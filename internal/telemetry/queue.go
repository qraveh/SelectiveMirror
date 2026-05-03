// Durable on-disk queue for telemetry events.
//
// **STATUS (FINDING 17, round-5 validation memo, 2026-05-03):** this
// type is **scaffolding** for the install-event submit pipeline
// deferred to v1.0.x (see PRIVACY.md "Currently shipped vs.
// deferred"). It is currently **dead code in production** — no
// `cmd/` or `internal/` caller instantiates a Queue. It is retained
// in tree because:
//
//   - The first_seen / upgrade / reliability_snapshot submit pipeline
//     (FINDING 16) is the natural caller; deleting and re-adding the
//     queue when that lands would be churn for no benefit.
//   - The implementation is well-tested (queue_test.go) and the
//     atomic-rename + dead-letter semantics are non-trivial; rewriting
//     them in v1.0.x would re-incur the design cost.
//
// Maintenance constraint: do NOT wire a Queue to anything in `cmd/`
// without also implementing the install-event submit pipeline that
// FINDING 16 calls out. Half-wiring (e.g., a goroutine that drains
// the queue but no producer that fills it) makes "is install-census
// shipping" ambiguous and is the failure mode FINDING 16 already
// caught.
//
// Events are written to disk before any network I/O is attempted, so
// a crash, reboot, or transient outage doesn't lose them. The background
// dispatcher claims oldest-first and either Completes (success) or
// Releases (transient failure) or DeadLetters (permanently rejected).
//
// Layout:
//   <queueDir>/                      pending events (one file each)
//     <unix-nano>-<rand>.json
//   <queueDir>/sending/              currently being sent
//     <unix-nano>-<rand>.json
//   <queueDir>/dead-letter/          failed beyond retry; for inspection
//     <unix-nano>-<rand>.json
//
// Concurrency: Claim atomically renames into 'sending', so two goroutines
// (or two smirror processes) can't claim the same file. The single-instance
// lock smirror already holds means there should normally be one drainer
// per queue, but defense-in-depth.

package telemetry

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Queue is a durable disk-backed queue of pending telemetry submissions.
type Queue struct {
	dir string
}

// NewQueue creates a queue rooted at dir. Creates the directory and its
// 'sending' / 'dead-letter' subdirs if missing.
func NewQueue(dir string) (*Queue, error) {
	for _, sub := range []string{"", "sending", "dead-letter"} {
		path := filepath.Join(dir, sub)
		if err := os.MkdirAll(path, 0o700); err != nil {
			return nil, fmt.Errorf("telemetry queue: mkdir %s: %w", path, err)
		}
	}
	return &Queue{dir: dir}, nil
}

// Enqueue writes a payload to the queue. The payload is the full ready-
// to-send body (HMAC already computed; canonical JSON serialized; etc.).
//
// Returns the path of the queued file (useful for logging or testing).
// Atomic: writes to a .tmp file first, then renames into place, so a
// concurrent Claim cannot see a partially-written file.
func (q *Queue) Enqueue(payload []byte) (string, error) {
	name, err := nextFilename()
	if err != nil {
		return "", err
	}
	target := filepath.Join(q.dir, name)
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o600); err != nil {
		return "", fmt.Errorf("telemetry queue: write temp: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("telemetry queue: rename: %w", err)
	}
	return target, nil
}

// PendingCount returns the number of events currently queued (not in
// 'sending' or 'dead-letter'). Useful for monitoring / status output.
func (q *Queue) PendingCount() (int, error) {
	entries, err := os.ReadDir(q.dir)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			count++
		}
	}
	return count, nil
}

// DeadLetterCount returns the number of files in the dead-letter subdir.
func (q *Queue) DeadLetterCount() (int, error) {
	entries, err := os.ReadDir(filepath.Join(q.dir, "dead-letter"))
	if err != nil {
		return 0, err
	}
	count := 0
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			count++
		}
	}
	return count, nil
}

// Claim atomically claims the oldest pending file for processing. Returns
// the new path (in 'sending') and the file's contents.
//
// The caller MUST eventually call Complete, Release, or DeadLetter on the
// returned path. If the caller crashes before that, SweepStaleClaims at
// next startup will recover the file.
//
// Returns ("", nil, nil) when the queue is empty (not an error).
func (q *Queue) Claim() (claimPath string, payload []byte, err error) {
	entries, err := os.ReadDir(q.dir)
	if err != nil {
		return "", nil, err
	}
	var candidates []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			candidates = append(candidates, e.Name())
		}
	}
	if len(candidates) == 0 {
		return "", nil, nil
	}
	sort.Strings(candidates) // unix-nano prefix → oldest-first

	src := filepath.Join(q.dir, candidates[0])
	dst := filepath.Join(q.dir, "sending", candidates[0])
	if err := os.Rename(src, dst); err != nil {
		// Race with another claimer (concurrent goroutine, manual rm,
		// etc.) — surface as transient.
		return "", nil, fmt.Errorf("telemetry queue: claim rename: %w", err)
	}
	payload, err = os.ReadFile(dst)
	if err != nil {
		return "", nil, fmt.Errorf("telemetry queue: read after claim: %w", err)
	}
	return dst, payload, nil
}

// Complete marks a claimed file as successfully sent and removes it.
// Idempotent: if the file is already gone, returns nil.
func (q *Queue) Complete(claimPath string) error {
	if err := os.Remove(claimPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("telemetry queue: complete: %w", err)
	}
	return nil
}

// Release puts a claimed file back into the pending queue, e.g. after a
// transient failure. The file's modtime is updated, but its name (which
// encodes original enqueue time) is preserved — so backoff is the
// dispatcher's responsibility (sleep before re-Claim), not the queue's.
func (q *Queue) Release(claimPath string) error {
	base := filepath.Base(claimPath)
	dst := filepath.Join(q.dir, base)
	if err := os.Rename(claimPath, dst); err != nil {
		return fmt.Errorf("telemetry queue: release rename: %w", err)
	}
	return nil
}

// DeadLetter moves a claimed file to the dead-letter subdir for manual
// inspection. Used after exhausting retries.
func (q *Queue) DeadLetter(claimPath string) error {
	base := filepath.Base(claimPath)
	dst := filepath.Join(q.dir, "dead-letter", base)
	if err := os.Rename(claimPath, dst); err != nil {
		return fmt.Errorf("telemetry queue: dead-letter rename: %w", err)
	}
	return nil
}

// SweepStaleClaims moves any files in 'sending' older than maxAge back to
// the pending queue. Recovers from crashes that left files claimed but
// unfinished. Intended caller: the install-event submit pipeline at
// startup (deferred to v1.0.x per FINDING 16; today there is no
// startup caller — see the package docstring's STATUS note).
func (q *Queue) SweepStaleClaims(maxAge time.Duration) (int, error) {
	sendingDir := filepath.Join(q.dir, "sending")
	entries, err := os.ReadDir(sendingDir)
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().Add(-maxAge)
	swept := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			src := filepath.Join(sendingDir, e.Name())
			dst := filepath.Join(q.dir, e.Name())
			if err := os.Rename(src, dst); err == nil {
				swept++
			}
		}
	}
	return swept, nil
}

// PurgeAll deletes every queued file (pending, sending, dead-letter).
// Intended caller: `smirror telemetry none` (the opt-out subcommand;
// the earlier `telemetry off` doc comment was a stale reference to a
// subcommand that never shipped under that name — fixed in 0.9.97-dev
// per FINDING 17). Drops unsent events when the user revokes consent.
//
// Returns the number of files deleted.
func (q *Queue) PurgeAll() (int, error) {
	count := 0
	for _, sub := range []string{"", "sending", "dead-letter"} {
		dir := filepath.Join(q.dir, sub)
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return count, err
		}
		for _, e := range entries {
			if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
				if err := os.Remove(filepath.Join(dir, e.Name())); err == nil {
					count++
				}
			}
		}
	}
	return count, nil
}

// nextFilename returns "<unix-nano>-<rand>.json", suitable for sort-by-name
// ordering to be chronologically consistent.
func nextFilename() (string, error) {
	var rnd [4]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d-%s.json", time.Now().UnixNano(), hex.EncodeToString(rnd[:])), nil
}
