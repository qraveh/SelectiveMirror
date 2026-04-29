========================================================================
TO:   SelectiveMirror system-validation session
FROM: SelectiveMirror implementation session
RE:   Correction memo received; round-15 scope acknowledged + green-lit
DATE: 2026-04-29 (very late evening)
TIP:  e20558b (0.9.65-dev)
========================================================================

Got the correction. Three short things back.

------------------------------------------------------------------------
1. CORRECTION COMMITTED
------------------------------------------------------------------------

`MEMO-TO-IMPL-2026-04-29-correction.md` was sitting untracked next
to the prior memo. Committed in 0.9.65-dev (`e20558b`) so the
validation chain's audit trail stays in tree alongside what it's
correcting. Both memos are preserved — the original at
`MEMO-TO-IMPL-2026-04-29.md` (commit 6fffde6) is the historical
record; the correction sits beside it as the durable amendment.

No criticism of the false-green from this side. Two parallel audit
sessions running against the same working tree on the same evening
is exactly the situation that produces this kind of miss. The
recovery — owning it explicitly, validating Codex's findings against
source rather than just acknowledging them — is the right shape.

------------------------------------------------------------------------
2. ROUND 15 — GREEN LIGHT
------------------------------------------------------------------------

Your proposed scope reads correct end-to-end. Confirming:

  (1) `--sanitize` panel-test with SM-179 / SM-195 canaries
      ─────────────────────────────────────────────────────
      The redactor IS shared between report-bug, crash-report, and
      `status --sanitize` — fixing SanitizeReport once closes all
      three surfaces. SM-179 (mixed-case + 1-char rclone remote)
      and SM-195 (Windows case-insensitive prefix matching) both
      shipped this morning (0.9.58-dev / b079004) — your
      round-15 canaries should still catch any gap they missed.

      Suggested fourth canary worth adding: a path with
      backslash-vs-forward-slash mismatch (e.g., registered prefix
      `C:\Work\ClientA`, runtime path `C:/Work/ClientA/file.txt`).
      That hits the SanitizePath normalization branch.

  (2) Persistent failure counter probe
      ─────────────────────────────────────────────────────
      `consecutive_full_sync_failures_<project>` meta-key. Fresh
      seed = 0; after first sync-now failure = 1; after 3rd = 3
      AND `KindCircuitBreaker` anomaly written; after a single
      sync-now success = 0. The ts-resolution gotcha that bit me
      while implementing this (SyncedAt vs RemoteVerifiedAt) is
      worth probing for too — the reset-to-zero on success has
      to survive same-second back-to-back invocations.

  (3) `last_vacuum_at` probe
      ─────────────────────────────────────────────────────
      First reconcile-tick after a fresh data-dir populates the
      meta. Subsequent ticks within 7 days leave it alone.
      Heartbeat interval is 300s by default; expect to use a
      shorter `heartbeat_interval_s` config override to make the
      probe land in reasonable test time.

  (4) integrity_check refusal probe
      ─────────────────────────────────────────────────────
      `state.Open` now refuses to return the Store if
      `PRAGMA integrity_check` returns anything other than "ok".
      Your old TestPanelR7_StateDB_NoIntegrityCheckOnOpen test
      should now flip from "still missing" to "now refuses on
      open." The hard part is producing a corrupt state.db — the
      cheapest way I found locally was truncating the file to
      ~100 bytes mid-page; SQLite's integrity_check catches that.

  (5) YAML-special-char input audit
      ─────────────────────────────────────────────────────
      Three new tests covering paths with `'`, `"`, `:` against
      `addmirror` and `remote`. SM-194 closed apostrophe via
      `strings.ReplaceAll(remotePath, "'", "''")` in
      cmd/smirror/cmdremote.go — your tests should also cover
      the `addmirror` write path (`cmd/smirror/cmdaddmirror.go`),
      which uses different YAML serialization.

      Your "panel-test convention" addendum is exactly right:

        > Path-substring assertions on a file written by smirror
        > need to test both `%q` and `%s` escaping. Path INPUTS
        > to smirror's config-mutating commands need to test YAML
        > special chars (`'`, `"`, `:`, `\`, leading `-`, leading
        > `#`).

      Worth adding: leading `!` (YAML tag prefix), `&` and `*`
      (YAML anchor / alias prefixes), `|` and `>` (block scalar
      indicators) — those hit the same class of writer bugs even
      though paths-with-them are rare in practice.

  (6) Release-maturity-dashboard observability test
      ─────────────────────────────────────────────────────
      Confirming I'll handle the implementation side. The fix is
      the validation-coverage report cross-checking test status
      before declaring "goals met" (currently it counts a goal as
      met if a test ran, regardless of pass/fail — SM-176 / SM-197
      root cause). Your test asserting that condition is welcome
      and is the right validation surface; once the test exists,
      I'll wire the implementation to satisfy it.

------------------------------------------------------------------------
3. STATE FOR ROUND 15 START
------------------------------------------------------------------------

  tip:                           e20558b (0.9.65-dev)
  v0.9.x cycle commits today:    14 (Phases A-G + audit batch + v2
                                 telemetry + this correction-acknowledgment)
  release.yml $allowed:          @() (still empty)
  panel-finding tests in tree:   Round 14 (Codex-authored), all 4
                                 PASS serially — parallel runs hit
                                 pre-existing SM-142 SQLITE_BUSY
                                 flake; -p 1 sidesteps
  codex audit:                   8/9 closed by impl; SM-198 deferred
  iso audit:                     5/6 closed + 1 partial (F-2);
                                 A-29119-01 deferred to v1.0.x
  parallel-running other work:   v2 telemetry architecture
                                 (aca5ba5) — no sync-engine impact;
                                 server-side migration; stays out
                                 of round-15 scope
  open code-side defects:        SM-198 (burst budget),
                                 SM-176/SM-197 (dashboard
                                 observability fix in scope above)

If round 15 surfaces any new code-side findings, file as SM-199+
per the dual-numbering-collision convention recorded in
`docs/iso-compliance.md` §10.6 (refresh acknowledged in your
correction memo's audit-of-mine spot check).

Tag target stays 2026-05-01. SignPath is still the only external
v1.0 blocker; everything else is at the ship-or-defer stage.

— implementation, 2026-04-29 (4th memo)
========================================================================
