# rclone Stall Detection — design v2 (post-review)

**Status**: revised after multirole BMad panel (Winston / Amelia / Adversarial / Edge-case Hunter), 2026-04-27.
**Author**: Claude (with project owner Raveh).
**Predecessor**: design v1 (kept in git history of this file).

This v2 incorporates the panel's findings. See the synthesis section at the bottom of the conversation transcript for line-by-line resolution.

---

## 1. Context

SelectiveMirror is a Windows-first service that watches local files and mirrors them to rclone-supported backends. Every sync operation spawns an `rclone` subprocess. Today, every invocation is wrapped in a hard 5-minute `context.WithTimeout` — wrong in both directions (too short for legitimate large transfers, too long for hung metadata ops, fatally short for batch syncs).

## 2. Constraints

- **No "use a different strategy" failure mode.** The system must let huge legitimate operations run as long as they keep proving they need to. No human or agent on the user side can interpret "switch tactics."
- **Specific, dynamic, context-aware** thresholds. Decide based on observed evidence, not on an arbitrary clock.
- **Windows-only at runtime.** Tests run on the dev box; the supervisor's signal probes live in a Windows-only file.

## 3. Two-layer design

### Layer 1 — let rclone fail itself (primary)

Configure rclone with self-failure flags so it exits non-zero on persistent failure inside its own retry layer:

| Flag | Value | Rationale |
|---|---|---|
| `--contimeout` | 30s | TCP connect timeout |
| `--timeout` | 60s | Idle TCP read timeout |
| `--retries` | 3 | Operation-level retries (existing) |
| `--low-level-retries` | 3 | HTTP-level retries — **dialed down from rclone default 10** so worst-case in-rclone time stays bounded |
| `--retries-sleep` | 10s | Backoff between op retries (existing) |

**Worst-case in-rclone time** with these settings: `3 ops × (3 low-level × 60s read + 30s connect + 10s sleep) ≈ 12 minutes`. During those 12 minutes, rclone's `--stats=15s --stats-one-line` (auto-injected for transfer verbs) keeps the `output` signal active, preventing Layer 2 from firing prematurely on legitimate retries.

**Collision detection.** Before injection, scan `cfg.RcloneExtraFlags` and `proj.RcloneExtraFlags` for any of `--contimeout`, `--timeout`, `--retries`, `--low-level-retries`, `--retries-sleep`. If user already set one, skip our injection for that flag and emit a warn log. Last-wins is unreliable across rclone versions for some flags.

**rclone version compat.** All five flags exist in rclone ≥ 1.43 (well below project minimum 1.73). No version gating needed.

### Layer 2 — three-signal OR observer (backstop)

For when rclone is wedged below its own retry layer (kernel NIC stall, Windows file-system filter-driver hang, AV holding a lock, HTTP/2 stream stuck in the runtime) — a multi-signal observer:

| Signal | Source | Heartbeat character |
|---|---|---|
| `output` | `io.MultiWriter` tee on stdout+stderr (atomic.Int64 timestamp) | `--stats=15s` provides cadence during transfers; stderr lines on errors otherwise |
| `cpu_time` | Windows `GetProcessTimes(handle)` returning kernel + user time | OS witness that rclone is computing |
| `io_bytes` | Windows `GetProcessIoCounters` (loaded via `kernel32.dll.NewProc`) returning Read+Write+Other transfer counts | OS witness that rclone is doing I/O at any layer |

`dst_size` from design v1 is **dropped**. (Production almost never has a local-fs target; detection-from-args was unspecified; sparse files break the monotonicity assumption.)

**Decision rule (OR / reset semantics)**:
1. At each sample tick, capture deltas vs. previous tick.
2. If **any one** signal shows movement → reset the stall counter to 0.
3. If **all three** are flat → increment the stall counter.
4. When stall counter ≥ K → kill via `sync.Once`-guarded `cmd.Process.Kill()` and record `Sync:Stalled` anomaly.

The OR makes Layer 2 ignore legitimate retry-sleeps (because `--stats` keeps output ticking) while still firing when rclone is genuinely wedged (output, cpu, AND io all stop).

### Two buckets

| Bucket | Verbs | Sample interval | K | Effective grace |
|---|---|---|---|---|
| **Transfer** | `copyto`, `copy`, `sync`, `moveto`, `touch` | 10s | 6 | 60s of nothing |
| **Metadata** | `lsjson`, `deletefile`, `purge` | 30s | 8 | 240s of nothing |

Bucket selected once at supervisor start from a `RcloneInvocation` struct constructed by callers and threaded into the runner. The single source of truth for verb classification is in `liveness.go::bucketFor(verb)`.

**File size doesn't affect bucket choice.** A 1 MB file and a 1 GB file get the same 60s grace — because the question Layer 2 answers ("is anything happening at all?") is size-independent. Size only affects expected duration, not stall threshold.

**Project file count doesn't affect bucket choice either.** An lsjson on a project with 100 entries and one with 100k entries get the same 240s grace — for the same reason.

## 4. Process lifecycle

```
cmd.Start()
  → OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
  → defer windows.CloseHandle(handle)
  → spawn waiter goroutine: cmd.Wait(); close(done)
  → spawn supervisor: ticker, signal sampling, kill-when-K-flat
  → main goroutine selects on:
      done: process exited (kill-or-clean) → return
      ctx.Done: parent cancelled → killOnce → wait for done → return
      supervisor.killC: stall detected → killOnce → wait for done → return
```

`sync.Once` ensures only one Kill call lands. The supervisor never calls Kill if the process has already exited (gated by `done`-channel select before the kill).

## 5. Anomaly differentiation

- **`Sync:Stalled`** (new). Multi-signal flatline detected; rclone wedged below its own retry layer. Severity: warning.
- **`Sync:LsJsonSlow`** (new). lsjson exceeded 60s but is still showing signal movement. Severity: info. Awareness signal — not a kill.
- **`Sync:Timeout`** (existing). No new emissions. Will be reachable only via the legacy `SMIRROR_DISABLE_LIVENESS=1` path (one-release escape hatch).

This avoids silently shifting the meaning of the existing kind. Operators reading anomaly logs can distinguish "watchdog killed it" from "wall-clock deadline expired."

## 6. RcloneInvocation struct

```go
// RcloneInvocation describes a single rclone subprocess invocation for the
// supervisor's bucket selection. Constructed by callers; passed through
// runRclone. Empty values are tolerated and produce conservative defaults.
type RcloneInvocation struct {
    Verb         string  // "copyto", "lsjson", "purge", etc.
    LocalSize    int64   // file size in bytes for transfers; 0 if N/A
    Project      string  // for diagnostic context only
    ProjectFiles int64   // state DB row count; for diagnostic context only
}
```

Threaded through six call sites (syncSingleFile, syncMtime, syncFullProject, deleteRemoteFile, deleteRemoteDir, ghost cleanup, lsjson list). The runner's existing `runRclone(ctx, args)` becomes a thin wrapper for tests that don't care about liveness — it constructs a default `RcloneInvocation{}`.

## 7. Migration

Single env-var kill-switch: `SMIRROR_DISABLE_LIVENESS=1` reverts to the old 5-minute `context.WithTimeout` path. One release window. After that, the legacy path is deleted.

No `--legacy-timeout` CLI flag — that would imply we plan to keep both paths. We don't.

## 8. Test seams

Two seams on `Engine`:

```go
// LivenessTickC, if non-nil, replaces the internal time.Ticker for the
// supervisor's sample loop. Tests advance time by sending on this channel.
LivenessTickC chan time.Time

// LivenessProbe, if non-nil, replaces the real GetProcessTimes /
// GetProcessIoCounters readings. Tests substitute scripted values.
LivenessProbe func(handle uintptr) Signals
```

No `Clock` interface diffusing through the package. No build-tag `proc_other.go` — the `LivenessProbe` field swap covers cross-platform tests on the dev box. (`proc_windows.go` provides the production probe via build tag.)

## 9. Acceptance criteria (from Amelia, slightly tightened)

- **AC-LV-01** `runRclone` no longer wraps the rclone subprocess in `context.WithTimeout(5min)` unless `SMIRROR_DISABLE_LIVENESS=1`.
- **AC-LV-02** `commonFlags` includes `--contimeout 30s --timeout 60s --low-level-retries 3` in addition to existing flags. `deleteFlags` includes the same except `--low-level-retries`.
- **AC-LV-03** Collision detection: any of the five injected flags already present in user-supplied `RcloneExtraFlags` skips that single injection and warn-logs.
- **AC-LV-04** `RcloneInvocation` struct constructed at each rclone call site. Test asserts hint matches expectation per call site.
- **AC-LV-05** Bucket selection is pure-function table: known transfer verbs → Transfer bucket; known metadata verbs → Metadata bucket; unknown verb → Metadata bucket (conservative default) + warn log.
- **AC-LV-06** Supervisor kills the process when **all three** signals are flat for K consecutive ticks. Event-based test: fake probe via channel, supervisor kills exactly on the K-th flat tick. No `time.Sleep`; no `time.After` for assertions.
- **AC-LV-07** **Any one** signal showing motion resets the stall counter. Parameterised test ×3: each signal moves alone while others stay flat.
- **AC-LV-08** On kill, `Sync:Stalled` anomaly recorded with detail capturing bucket, K, interval, and per-signal last-motion timestamps.
- **AC-LV-09** After kill, `cmd.Wait()` returns within 2s; no goroutine leak.
- **AC-LV-10** `syncMtime` continues to write `rclone_exit=0` to state DB regardless of supervisor outcome (SM-048 invariant preserved).
- **AC-LV-11** Stderr buffer still captured for diagnostics when supervisor kills.
- **AC-LV-12** User-supplied `RcloneExtraFlags --retries-sleep 60s` overrides our `10s` (last-wins via skip-injection).
- **AC-LV-13** `SMIRROR_DISABLE_LIVENESS=1` reverts to the 5-min `context.WithTimeout` path; supervisor not started.
- **AC-LV-14** Process exits cleanly between samples → no kill, no `Sync:Stalled`. Test: fake runner exits between tick N and tick N+1.
- **AC-LV-15** Parent ctx cancelled mid-sample → kill via `sync.Once`; supervisor exits within 2s.
- **AC-LV-16** lsjson elapsed > 60s with signals moving → `Sync:LsJsonSlow` info anomaly. Doesn't kill. Doesn't fire again for the same op.

## 10. Implementation cost (revised)

- `internal/sync/liveness.go` — supervisor + bucket selector + `RcloneInvocation` + `Signals` struct + decision loop (~150 LOC)
- `internal/sync/proc_windows.go` (build tag `windows`) — `GetProcessTimes` wrapper + `GetProcessIoCounters` via `LazyDLL.NewProc` + `unsafe.Pointer` cast (~50 LOC)
- `internal/sync/proc_other.go` (build tag `!windows`) — stub returning zero `Signals` (~15 LOC)
- Refactor `internal/sync/sync.go::defaultRunRclone` — drop hard timeout, integrate supervisor, kill-switch handling (~80 LOC delta)
- `commonFlags` / `deleteFlags` flag injection + collision detection (~30 LOC)
- Six call-site updates constructing `RcloneInvocation` (~30 LOC)
- Anomaly kinds added in `internal/anomaly/anomaly.go` (~10 LOC)
- Tests `internal/sync/liveness_test.go` (~250 LOC)

**Total ~615 LOC.** Higher than v1's 320 — Amelia's recalibration was correct.

## 11. What's deliberately deferred

- **Per-mirror override** of bucket parameters — defer until someone asks.
- **State-DB-derived historical p99 thresholds** — requires N successful syncs to bootstrap; v1 buckets are simpler and converge to the same outcome on healthy backends.
- **Adaptive sample interval** (decay 5s → 15s) — defer.
- **Linux/macOS production probes** — project is Windows-only at runtime; `proc_other.go` returns zeros.
