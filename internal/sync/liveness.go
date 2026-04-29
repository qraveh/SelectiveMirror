package sync

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	gosync "sync"
	"sync/atomic"
	"time"

	"github.com/qraveh/SelectiveMirror/internal/anomaly"
)

// RcloneInvocation describes a single rclone subprocess for the supervisor's
// bucket selection. Constructed by callers; threaded through runRclone.
// Empty / unknown values are tolerated and produce conservative defaults.
type RcloneInvocation struct {
	Verb         string // "copyto", "lsjson", "purge", etc. (= args[0])
	LocalSize    int64  // file size in bytes for transfers; 0 if N/A
	Project      string // for diagnostic context only
	ProjectFiles int64  // state-DB row count; for diagnostic context only
}

// Signals holds the per-tick liveness sample. Empty values represent
// "unknown / not sampled" — distinct from zero ("sampled and was zero").
// The supervisor uses the previous-tick comparison only for fields that
// the probe successfully read this tick; any field that errored is
// excluded from the AND-flat decision (treated as unknown, not flat).
type Signals struct {
	CPUTimeNs uint64 // GetProcessTimes(kernel + user)
	IOBytes   uint64 // GetProcessIoCounters(read + write + other)
}

// LivenessProbe reads the OS-level liveness signals for a given process
// handle. The production implementation is realLivenessProbe in
// proc_windows.go; tests inject a fake.
type LivenessProbe func(handle uintptr) (Signals, error)

// stderrBufCap is the maximum number of bytes captured into the
// per-rclone stderr buffer. Tier-2 #19 (validation panel 2026-04-29):
// `rclone --verbose --verbose` against a backend with 100K files can
// emit hundreds of MB of stderr; capturing it all into a strings.Builder
// is a heap-blowup vector that has no upside (we only need a slice for
// the failure log line). 64 KB is more than enough for the diagnostic.
const stderrBufCap = 64 * 1024

// boundedStderrWriter is an io.Writer that captures up to stderrBufCap
// bytes into an internal strings.Builder; further bytes are silently
// dropped. The String() method appends a truncation marker once the
// cap is reached so the diagnostic remains honest.
type boundedStderrWriter struct {
	buf       strings.Builder
	cap       int
	truncated bool
}

func newBoundedStderr() *boundedStderrWriter {
	return &boundedStderrWriter{cap: stderrBufCap}
}

func (b *boundedStderrWriter) Write(p []byte) (int, error) {
	if b.cap <= 0 {
		return b.buf.Write(p)
	}
	remaining := b.cap - b.buf.Len()
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		b.buf.Write(p[:remaining])
		b.truncated = true
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *boundedStderrWriter) String() string {
	if !b.truncated {
		return b.buf.String()
	}
	return b.buf.String() + fmt.Sprintf("\n...[stderr truncated at %d bytes]", b.cap)
}

// activityWriter is an io.Writer that records the timestamp of the latest
// write into an atomic int64 (unix nanoseconds). Used as one branch of
// io.MultiWriter on rclone's stdout/stderr — every byte rclone produces
// is a heartbeat.
//
// The writer never errors and never blocks; it discards content (the other
// branches of MultiWriter — os.Stdout, &stderrBuf — keep the actual data).
type activityWriter struct {
	lastNs   *atomic.Int64
	lastLine *atomicString // last non-empty line of output, for diagnostic on kill
}

func (w *activityWriter) Write(p []byte) (int, error) {
	w.lastNs.Store(time.Now().UnixNano())
	if w.lastLine != nil && len(p) > 0 {
		// Record the last reasonable-length line for the kill diagnostic.
		// Don't bother parsing — just remember the tail.
		s := string(p)
		s = strings.TrimRight(s, "\r\n")
		if len(s) > 256 {
			s = s[len(s)-256:]
		}
		if s != "" {
			w.lastLine.Store(s)
		}
	}
	return len(p), nil
}

// atomicString is a tiny wrapper around atomic.Value for string storage.
type atomicString struct {
	v atomic.Value
}

func (a *atomicString) Store(s string) { a.v.Store(s) }
func (a *atomicString) Load() string {
	v := a.v.Load()
	if v == nil {
		return ""
	}
	return v.(string)
}

// Bucket holds the supervisor's tick parameters for a class of operation.
type Bucket struct {
	Name     string        // for diagnostic: "transfer" / "metadata"
	Interval time.Duration // sample interval
	K        int           // consecutive flat ticks before kill
}

var (
	transferBucket = Bucket{Name: "transfer", Interval: 10 * time.Second, K: 6} // ~60s grace
	metadataBucket = Bucket{Name: "metadata", Interval: 30 * time.Second, K: 8} // ~240s grace
)

// bucketFor returns the supervisor bucket for an rclone verb. Unknown
// verbs default to metadata (the more permissive bucket) — better to
// over-tolerate an unknown op than to falsely kill it.
func bucketFor(verb string) Bucket {
	switch verb {
	case "copyto", "copy", "sync", "moveto", "touch":
		return transferBucket
	case "lsjson", "deletefile", "purge":
		return metadataBucket
	default:
		return metadataBucket
	}
}

// killReason is one of: "exit-natural" (process self-exited), "ctx-cancel"
// (parent context cancelled), "stalled" (supervisor decided all signals
// flat). Used for the anomaly emission and exit-code mapping.
type killReason int

const (
	killExitNatural killReason = iota
	killCtxCancel
	killStalled
)

// runWithSupervisor runs cmd under the multi-signal stall supervisor.
//
// Returns:
//
//	rcloneExit:     the process exit code, OR -2 for stall-killed,
//	                OR -3 for context-cancelled.
//	stderrCapture:  stderr collected during the run (regardless of outcome).
//
// The supervisor logic:
//   - Tee stdout+stderr through an activityWriter that timestamps every byte.
//   - Open an OS handle on the rclone process for cpu/io probes.
//   - Tick at bucket.Interval. At each tick, compare three signals against
//     the previous tick: output (last activity timestamp), CPUTimeNs,
//     IOBytes. ANY signal moved → reset the stall counter to 0. ALL three
//     flat → increment. When stall counter ≥ bucket.K → kill.
//   - sync.Once on Kill. Wait() always called (drains os/exec's pipe-reader
//     goroutines so we don't leak fds).
//   - On a stall kill, emit Sync:Stalled anomaly with bucket diagnostic.
//   - LsJsonSlow info anomaly if metadata bucket and elapsed > lsJsonSlowAt.
func runWithSupervisor(
	ctx context.Context,
	cmd *exec.Cmd,
	inv RcloneInvocation,
	probe LivenessProbe,
	tickC <-chan time.Time, // injectable for tests; if nil, use real ticker
	rec *anomaly.Recorder,
	logFn func(level, msg string, fields ...any),
) (rcloneExit int, stderrCapture string) {
	bucket := bucketFor(inv.Verb)

	// Activity tracker — every byte rclone writes updates the timestamp.
	var lastActivityNs atomic.Int64
	lastActivityNs.Store(time.Now().UnixNano())
	var lastLine atomicString
	activity := &activityWriter{lastNs: &lastActivityNs, lastLine: &lastLine}

	// Stderr capture preserved for diagnostic logging. Stdout still routed
	// to os.Stdout for foreground operators. Tier-2 #19: capture is capped
	// at stderrBufCap to defend against rclone --verbose blowing up heap.
	stderrBuf := newBoundedStderr()
	cmd.Stdout = io.MultiWriter(os.Stdout, activity)
	cmd.Stderr = io.MultiWriter(activity, stderrBuf)

	if err := cmd.Start(); err != nil {
		stderrCapture = stderrBuf.String()
		return classifyStartError(err), stderrCapture
	}

	// Open the OS handle for sampling. If this fails, we degrade to
	// output-only signal — still better than no supervisor at all.
	var handle uintptr
	var handleOK bool
	if probe != nil && cmd.Process != nil {
		h, err := openProcessHandle(cmd.Process.Pid)
		if err == nil {
			handle = h
			handleOK = true
			defer closeProcessHandle(handle)
		} else if logFn != nil {
			logFn("debug", "openProcessHandle failed; supervisor degrading to output-only signal",
				"pid", cmd.Process.Pid, "error", err.Error())
		}
	}

	// Wait goroutine — populates done when the process exits naturally.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	// Kill machinery — sync.Once ensures one Kill call regardless of which
	// path triggers it. killReasonHolder records why we killed for the
	// post-mortem anomaly.
	var killOnce gosync.Once
	var killReasonHolder killReason = killExitNatural
	doKill := func(reason killReason) {
		killOnce.Do(func() {
			killReasonHolder = reason
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		})
	}

	// Internal ticker if test didn't provide one. Tests pass a controlled
	// channel and never use the real clock.
	var ticker *time.Ticker
	var ownedTickC <-chan time.Time
	if tickC == nil {
		ticker = time.NewTicker(bucket.Interval)
		defer ticker.Stop()
		ownedTickC = ticker.C
	} else {
		ownedTickC = tickC
	}

	// Previous-tick state. Initialized lazily on the first probe success.
	type sample struct {
		outputNs  int64
		cpuNs     uint64
		ioBytes   uint64
		probedOK  bool // true once the probe has succeeded at least once
	}
	var prev sample
	prev.outputNs = lastActivityNs.Load()

	stallCounter := 0
	startTime := time.Now()
	lsJsonSlowEmitted := false
	const lsJsonSlowAt = 60 * time.Second

	for {
		select {
		case waitErr := <-done:
			// Process exited. classifyExit translates kill-vs-natural into
			// the int returned to callers.
			stderrCapture = stderrBuf.String()
			return classifyExit(cmd, waitErr, killReasonHolder, lastLine.Load(), bucket, stallCounter, rec, inv, logFn), stderrCapture

		case <-ctx.Done():
			doKill(killCtxCancel)
			waitErr := <-done
			stderrCapture = stderrBuf.String()
			return classifyExit(cmd, waitErr, killReasonHolder, lastLine.Load(), bucket, stallCounter, rec, inv, logFn), stderrCapture

		case <-ownedTickC:
			// LsJsonSlow advisory anomaly (info, not a kill).
			if !lsJsonSlowEmitted && inv.Verb == "lsjson" && time.Since(startTime) > lsJsonSlowAt {
				if rec != nil {
					rec.Record(anomaly.KindSyncLsJsonSlow, inv.Project, "",
						fmt.Sprintf("lsjson elapsed %s — still alive, no kill", time.Since(startTime).Round(time.Second)),
						fmt.Sprintf("project_files=%d, bucket=%s", inv.ProjectFiles, bucket.Name))
				}
				lsJsonSlowEmitted = true
			}

			// Sample current signals.
			cur := sample{outputNs: lastActivityNs.Load()}
			probeOK := false
			if handleOK && probe != nil {
				if sigs, err := probe(handle); err == nil {
					cur.cpuNs = sigs.CPUTimeNs
					cur.ioBytes = sigs.IOBytes
					probeOK = true
				}
			}

			// Decide. ANY signal moved → reset counter.
			//   - output moved: cur.outputNs > prev.outputNs
			//   - cpu moved:    probeOK && cur.cpuNs > prev.cpuNs (only if both ticks probed OK)
			//   - io moved:     probeOK && cur.ioBytes > prev.ioBytes (same)
			anyMoved := cur.outputNs > prev.outputNs
			if probeOK && prev.probedOK {
				if cur.cpuNs > prev.cpuNs {
					anyMoved = true
				}
				if cur.ioBytes > prev.ioBytes {
					anyMoved = true
				}
			}
			// If the probe errored this tick (or any past tick), we treat
			// cpu/io as "unknown" — neither flat nor moving. Output alone
			// is still meaningful.
			if !probeOK {
				// Probe failed this tick. Keep prev.cpu/io unchanged so a
				// future successful probe can compare. Output remains
				// authoritative.
			}

			if anyMoved {
				stallCounter = 0
			} else {
				stallCounter++
			}

			// Update prev for next tick. Only carry over fields we sampled
			// successfully; output we always have.
			prev.outputNs = cur.outputNs
			if probeOK {
				prev.cpuNs = cur.cpuNs
				prev.ioBytes = cur.ioBytes
				prev.probedOK = true
			}

			if stallCounter >= bucket.K {
				if logFn != nil {
					logFn("warn", "rclone subprocess stalled — killing",
						"verb", inv.Verb,
						"project", inv.Project,
						"bucket", bucket.Name,
						"k", bucket.K,
						"interval_s", int(bucket.Interval.Seconds()),
						"flat_for_s", int(time.Duration(stallCounter)*bucket.Interval/time.Second),
						"last_line", lastLine.Load())
				}
				doKill(killStalled)
				// Loop will pick up the done branch on next iteration.
			}
		}
	}
}

// classifyStartError translates a cmd.Start() failure into our exit code
// convention. Mirrors the existing defaultRunRclone semantics.
func classifyStartError(err error) int {
	// -1 is the existing code for "couldn't start".
	return -1
}

// classifyExit interprets the wait result + kill reason and emits the
// appropriate anomaly (Sync:Stalled on supervisor kill). Returns the
// integer exit code per existing convention:
//
//	-2 = stall-killed by supervisor
//	-3 = ctx cancelled
//	non-negative = rclone's own exit code
func classifyExit(
	cmd *exec.Cmd,
	waitErr error,
	reason killReason,
	lastLine string,
	bucket Bucket,
	stallCounter int,
	rec *anomaly.Recorder,
	inv RcloneInvocation,
	logFn func(level, msg string, fields ...any),
) int {
	switch reason {
	case killStalled:
		if rec != nil {
			detail := fmt.Sprintf("bucket=%s k=%d interval=%s flat=%s last_line=%q project_files=%d local_size=%d",
				bucket.Name, bucket.K, bucket.Interval,
				time.Duration(stallCounter)*bucket.Interval,
				lastLine, inv.ProjectFiles, inv.LocalSize)
			rec.Record(anomaly.KindSyncStalled, inv.Project, "",
				"rclone subprocess wedged below own retry layer (multi-signal flatline)",
				detail)
		}
		return -2
	case killCtxCancel:
		return -3
	}

	// Natural exit. Translate Go's exec error semantics into an int.
	if waitErr == nil {
		return 0
	}
	if exitErr, ok := waitErr.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	// Some other error from Wait — treat as -1 (general failure).
	if logFn != nil {
		logFn("debug", "rclone Wait returned non-ExitError", "err", waitErr.Error())
	}
	return -1
}
