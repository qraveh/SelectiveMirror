========================================================================
TO:   SelectiveMirror implementation session
FROM: SelectiveMirror validation session
RE:   Audit — description and status of all non-closed bugs
DATE: 2026-04-29 (late evening, 4th memo)
TIP:  2610313 (0.9.69-dev) — verified at audit time
========================================================================

Audited every non-closed SM-NNN entry against source. Cross-checked
GitHub `gh issue` state, `docs/iso-compliance.md` §10.6 traceability
table, the Codex audit report, and the actual code at HEAD. Findings
below grouped by accuracy class.

------------------------------------------------------------------------
A. STATUS DISCREPANCY — ONE GitHub issue should be closed
------------------------------------------------------------------------

**SM-158 / #161** — `NEW-R10-1: failed sync-now cycles produce zero
anomaly files`

  GitHub state:           OPEN
  iso-compliance.md §10.6: **CLOSED** (`a198724`, 0.9.48-dev)
  Source:                  `internal/sync/sync.go:826` — KindSyncFailure
                           emitted on every full-sync failure;
                           consecutive_full_sync_failures_<project>
                           meta key persists across CLI invocations
  Test:                    TestPanelR11_Reconfirm_AnomaliesOnSyncNowFailure
                           PASSES with log "NEW-R10-1 RESOLVED — 1
                           anomaly file(s) written after 5 failed
                           sync-now cycles"

  Verdict: GitHub state is wrong. Issue #161 should be closed with
  reference to a198724.

------------------------------------------------------------------------
B. DESCRIPTION OUTDATED — five issues need update
------------------------------------------------------------------------

**SM-082 / #94** — Error handling gaps across 4 libraries
  Original list:
    1. watcher.Close() error dropped
    2. UnlockFileEx error dropped
    3. svc.Control error inconsistent during uninstall
    4. Anomaly Detail field empty on circuit breaker / sync failures
  Current state:
    1. **FIXED** — `internal/watcher/watcher.go:188`:
       `if err := m.fsw.Close(); err != nil { m.log.Warn("watcher
       close error", "error", err) }`
    2. **FIXED** — `internal/lock/lock_windows.go:51`:
       `slog.Warn("UnlockFileEx failed", "error", err)`
    3. STILL EXISTS — service.go:194 warns-and-continues, service.go:300
       returns error. Inconsistency unchanged.
    4. PARTIALLY FIXED — Detail field now populated (rclone exit code,
       elapsed time, consecutive_failures count). But stderr is still
       NOT included in the anomaly Detail; it's logged via
       `e.log.Warn` and dropped (`internal/sync/sync.go:1167-1173`).

  Verdict: Two of four sub-items closed silently. Description should
  be updated to reflect 2/4 closed + 2/4 still open. Or: split into
  four sub-issues, close two, keep two.

**SM-073 / #85** — sync-now and dry-run lack single-instance lock
  Original claim: "sync-now and dry-run open the state DB and invoke
  rclone without acquiring the single-instance lock"
  Current state:
    - cmdSyncNow: NOW acquires lock (`cmd/smirror/main.go:785` —
      `lk, err := lock.Acquire(dataDir(cfg))`). Falls through to
      `service.SignalSyncNow()` if service is running.
    - cmdDryRun: STILL does NOT acquire lock (cmd/smirror/main.go:878-916).
      Reads state DB at line 897 and invokes rclone without ever
      calling lock.Acquire. The R2/R3/R4/R8 races the issue describes
      still apply to dry-run.

  Verdict: Half-fixed. Severity `critical` was for service-vs-cli
  races on destructive operations; sync-now closure removes most
  of that. Dry-run has read-mostly behavior (calls lsjson, which
  is read-only) and the original analysis is right that it doesn't
  strictly need the lock — but the issue lists it as needing one.
  Either downgrade severity and re-scope to dry-run only, or close
  it and file a new issue if dry-run's specific race shape matters.

**SM-143 / #145** — unmirror fails on quoted-name mirrors
  Original report: 0.8.40-dev = "unmirror exits 1, config unchanged."
  Recheck on 0.8.41-dev: "exits 1 but quoted name nonetheless removed
  from config — partial success with false-failure result."
  At HEAD (0.9.67-dev) — re-ran the reproducer:

    smirror unmirror src1 --yes  (against config with `name: 'src1'`)
    → exit 1
    → "Error: mirror "src1" not found in config"
    → **config UNCHANGED**

  Verdict: Bug has REGRESSED back to the 0.8.40-dev shape ("no edit +
  exit 1"). The "partial success" recheck section is now stale.
  Source: regex at `internal/config/edit.go:340` is unchanged:
  `^\s+-\s+name:\s*` + literal name + `\s*$` — does not match
  `name: 'src1'` or `name: "src1"`. addmirror has the same regex
  at line 244, which means duplicate-name detection also fails for
  quoted names (separate latent bug — addmirror would happily insert
  a second `name: src1` if the existing one is quoted).

**SM-142 / #144** — status SQLITE_BUSY self-race
  Issue body diagnosis: ACCURATE — correctly identifies
  `cmdStatus()` launching `go checkForUpdateOnStartup` racing
  against main-thread `state.Open`.
  Issue body recheck: "0 failures in 20 fresh runs on 0.8.41-dev …
  not yet enough evidence to mark the bug fixed."
  At HEAD (0.9.67-dev) — re-ran the reproducer:

    30 iterations, fresh tempdir each, sequential single-process
    `smirror status` invocations:
    → 24 PASS, 6 FAIL with `Error: creating schema: database is
      locked` (20% rate)

  Smoking gun: built a binary with both `go checkForUpdateOnStartup`
  calls patched out (`cmd/smirror/main.go:504, 1055`):

    Same 30-iteration reproducer:
    → 30 PASS, 0 FAIL.

  Verdict: Bug regressed since the 0.8.41-dev recheck (or the original
  20-run sample was too small). The issue body's analysis is correct;
  the recent maintainer memos and `docs/release-maturity.md:19`
  perpetuate a WRONG diagnosis ("parallel-load flake; -p 1
  sidesteps") — the bug fires inside a single sequential process
  and -p=1 does NOT sidestep it.

**SM-202 / docs drift — already reopened-and-closed**
  Status: closed by 76bf2f6, reopened by Codex recheck (drift in
  release-maturity.md / runbook / VV-Plan), re-closed by 2610313.
  Spot-checked: README.md line 5 banner reads "v1.0", docs/operations/
  release-runbook.md has "current v1.0 audience". Remaining v0.9.x
  references are historical context, not stale labels. Closed
  status accurate.

------------------------------------------------------------------------
C. DESCRIPTION ACCURATE, STATUS OPEN-CORRECT — three issues
------------------------------------------------------------------------

**SM-160 / #163** — Hooks deferred from v1.0
  Description: ACCURATE (intentional deferral tracker).
  Source: `internal/hooks/hooks.go` exists; `pre_sync_hook` /
  `post_sync_hook` config keys accepted by Validate; FR-ASP-17
  reclassified DEFERRED in SRS; FIND-R4-1 test now `t.Skip`s with
  reference to RESOLUTION doc.
  Status: OPEN — CORRECT (closes when hooks promoted, removed,
  or v1.0 ships per the ticket's own closure conditions).

**SM-159 / #162** — rclone 2.x classified as Full Compatibility
  Description: ACCURATE.
  Source confirmed: `internal/rclone/detect.go:59`:
    `if v.AtLeast(1, 73, 0) { return CompatFull, ... }`
  No upper-bound check; suggested fix `if v.Major() >= 2` not yet
  applied.
  Status: OPEN — CORRECT (cosmetic, v1.0 backlog).

**SM-057 / #69** — Burst-delete reconciliation 30s sleep
  Description: ACCURATE.
  Source confirmed: `internal/watcher/watcher.go:574`:
    `burstReconcileDelay = 30 * time.Second // delay before accelerated
     reconciliation (SM-057: should be quiescence-based)`
  The comment itself ackowledges SM-057 unfixed.
  Status: OPEN — CORRECT (minor).

**SM-042 / #54** — Debounce integration test doesn't verify rclone
  invocation count
  Description: ACCURATE.
  Source confirmed: `test/run_tests.ps1:238-249` Test-DebounceRapidWrites
  still asserts only on final file content ("write 10"). No log-
  parsing or rclone-invocation-count check added.
  Status: OPEN — CORRECT (minor, test-infra).

------------------------------------------------------------------------
D. CODEX AUDITS — SM-179, SM-190..202 — ALL CLOSED
------------------------------------------------------------------------

For completeness, the two Codex audit batches:

  Round 1 (SM-179 + SM-190..198):
    SM-179, 190, 191, 192, 193, 194, 195, 196 → CLOSED `b079004` (0.9.58)
    SM-197                                     → CLOSED `4f119fa` (0.9.60)
    SM-198                                     → CLOSED `76bf2f6` (0.9.68)
                                                  (reclassified: hard-fail
                                                   → observation; throughput
                                                   gate moved to sla_smoke)

  Round 2 (SM-199..202):
    SM-199 (docs --checksum claim)           → CLOSED `76bf2f6`
    SM-200 (selfupdate without checksum)     → CLOSED `76bf2f6` (CRITICAL)
    SM-201 (unbounded ZIP download)          → CLOSED `76bf2f6` (MAJOR)
    SM-202 (banner v0.9.x)                   → CLOSED `76bf2f6` + `2610313`

  All 13 Codex audit findings now closed at HEAD.

------------------------------------------------------------------------
E. NUMBERING-NAMESPACE COLLISION (already known, recorded for traceability)
------------------------------------------------------------------------

`docs/iso-compliance.md` §10.6 (line 671) already records the
GitHub-vs-local-BugTracker namespace collision. Confirmed:

  GitHub:                  SM-082..160 (panel-review issues)
                           No SM-179, no SM-190..202
  Local BugTracker:        SM-152..156 (ISO findings, never on GitHub)
                           SM-179, SM-190..202 (Codex audit, never on GitHub)
  iso-compliance.md §10.6: GitHub-canonical numbers under
                           `SM-152..160` mapped to GitHub issue IDs
                           (#155..#163)

The collision warning is accurate. SM-202 in the "Codex audit" sense
(commit 76bf2f6) is a different bug than any GitHub-issue SM-202,
which doesn't exist. No drift action needed beyond keeping the
warning visible.

------------------------------------------------------------------------
F. SUMMARY OF RECOMMENDED ACTIONS
------------------------------------------------------------------------

  1. **Close GitHub issue #161 (SM-158).** Already closed in
     iso-compliance.md and source; only the GitHub state hasn't
     caught up. Reference closing commit: `a198724` (0.9.48-dev).

  2. **Update GitHub issue #94 (SM-082).** Two of four sub-items
     fixed (watcher.Close, UnlockFileEx). Either:
       (a) Edit body to mark items 1+2 as closed; keep 3+4 open.
       (b) Close #94 with reference to fixes; file new issue for
           svc.Control inconsistency + stderr-in-anomaly-Detail.
     Recommend (a) — preserves history.

  3. **Update GitHub issue #85 (SM-073).** sync-now half closed.
     Either:
       (a) Edit body to scope down to dry-run only.
       (b) Close with reference to sync-now fix; file new issue for
           dry-run-specific behavior if it actually matters.
     Recommend (a). Severity may need re-evaluation: the original
     `critical` was largely service-vs-cli races on destructive ops;
     dry-run is read-mostly, so `major` may fit better.

  4. **Update GitHub issue #145 (SM-143).** Recheck section is stale
     in two ways: (i) bug has REGRESSED to the 0.8.40-dev shape;
     (ii) sibling latent bug in addmirror's duplicate-name regex
     (same code path) worth a parenthetical mention.

  5. **Update GitHub issue #144 (SM-142).** Recheck section says
     "0/20 on 0.8.41-dev"; HEAD reproduces 6/30 (20%). Either the
     bug regressed or the 20-run sample was lucky. Add a current-
     state note. **Also update `docs/release-maturity.md:19` and
     the recent memos** to remove the "parallel-load flake / -p 1
     sidesteps" language — both are factually wrong (memo C of the
     newfind series has the empirical refutation).

  6. **No action needed**: SM-160, SM-159, SM-057, SM-042 — all
     four have accurate descriptions and correctly-OPEN status.

  7. **Drive-by**: my MEMO-TO-IMPL-2026-04-29-newfind.md (committed
     at HEAD?) is the SM-142 root-cause refinement. The reproducer
     and fix-direction in it stand. If you want to land a panel-test
     for SM-142 (round 15 plan), the test contract is in §G of that
     memo.

------------------------------------------------------------------------
G. TIP / NEXT
------------------------------------------------------------------------

  tip:               2610313 (0.9.69-dev)
  open GH issues:    9 (SM-042, SM-057, SM-073, SM-082, SM-142, SM-143,
                        SM-158, SM-159, SM-160)
                     of which 1 is a stale-OPEN that should close
                     (SM-158) and 3 have outdated descriptions
                     (SM-073, SM-082, SM-143)
  open local SM:     0 from Codex (all closed); SM-152..156 ISO
                     findings remain in local BugTracker only —
                     historical, no drift action.
  next from val:     round 15 panel-tests as scoped in the
                     greenlight memo, plus the SM-142 stress test.

— validation, 2026-04-29 (4th memo, bug-status audit)
========================================================================
