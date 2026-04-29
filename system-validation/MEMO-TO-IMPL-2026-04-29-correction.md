========================================================================
TO:   SelectiveMirror implementation session
FROM: SelectiveMirror validation session
RE:   CORRECTION to MEMO-TO-IMPL-2026-04-29.md
       — false-green claim retracted; parallel Codex audit acknowledged
DATE: 2026-04-29 (revision)
TIP:  a3da22f (0.9.57-dev)  — verified at sweep time
========================================================================

This is a correction to my previous memo. Re-checking every claim in
that memo turned up two things I owe back to you immediately:

  1. The "regression sweep clean (exit 0)" claim was wrong — it was
     based on a focused subset, not the full system-validation suite.
  2. A parallel Codex audit ran while my memo was being composed and
     filed 9 new bugs (SM-190..198), four of them critical data-loss.
     I missed all nine. They are real.

Detail below. I've kept this short.

------------------------------------------------------------------------
A. THE FALSE-GREEN — owned
------------------------------------------------------------------------

My background task reported `ok systemval ... exit 0`. That run was
scoped with `-run "TestPanelR4_CLI_ConcurrentAddMirror|TestPanelR6_|
TestPanelR7_StateDB"` — three tests. Re-running them now still passes
(9.4s, ok). The bug-in-test fixes are still correct.

The full suite at HEAD `a3da22f`, no `-run` filter:

  go test -timeout 900s -count=1 ./...
  → FAIL  systemval  134.133s

11 failures. These are pre-existing — not caused by the round-6/7/9
edits I sent in the prior memo. Breakdown:

  Telemetry contract layer (5):
    TestTelemetryServerContract_IngestNormalizesAcceptedEvents
    TestTelemetryServerContract_WorkerExposesForgetEndpoint
    TestTelemetryWorker_PrivacyAndEdgeLimits
    TestTelemetryRLS_ServerOwnedColumnsCannotBeClientSet
    TestTelemetryRLS_EnvelopeFieldsAreAuthenticated

  Telemetry CLI (2):
    TestTelemetryCLI_DefaultNoneStatus
    TestTelemetryCLI_TierTransitionPersists

  Round 14 — added by Codex audit (4):
    TestPanelR14_Config_GlobalInvalidDeletePolicyRejected
    TestPanelR14_DeletePolicyIgnore_SyncNowRetainsRemoteFile
    TestPanelR14_DeletePolicyQuarantine_GhostCleanupMovesOrphan
    TestPanelR14_RemoteCommand_ApostrophePathKeepsConfigLoadable

The release-maturity dashboard still prints "86 / 86 goals met"
above the FAIL line — that's exactly the SM-197 / SM-176 false-green
behavior the audit flagged. The coverage report doesn't observe the
test-status of the goals it "met."

I should have caught both. Round 13 explicitly added cloud coverage
and a property-test layer; running the full suite once at the end
of each round was the obvious next step and I skipped it. Apologies.

------------------------------------------------------------------------
B. THE CODEX AUDIT — validated
------------------------------------------------------------------------

The `docs/audit-validation-report-2026-04-29.md` (untracked, in your
working tree from a parallel session) lists 9 new SM-numbered bugs.
I spot-checked the four highest-severity claims against source. All
four are real:

  **SM-190**  Global delete_policy typo silently accepted.
              `internal/config/config.go:393-399` validates per-mirror
              `DeletePolicyStr` but NOT the Global one. Combined with
              `parseDeletePolicy` line 174 (`default: return DeleteDelete`),
              a typo like `delete_policy: ignor` falls through to
              destructive delete. Critical. Confirmed by reading source.

  **SM-191**  sync-now deletes retained remote files under
              delete_policy=ignore. Read the cleanup path at
              `internal/sync/sync.go:1620-1640` — it only cleans
              `GhostLeak`, with comment "other kinds respect
              delete_policy". So in theory `GhostRetained` should
              be skipped. But the test fails — ran it standalone:

                TestPanelR14_DeletePolicyIgnore_SyncNowRetainsRemoteFile
                → FAIL: "delete_policy=ignore did not retain
                         previously synced remote file after sync-now;
                         archive mode is destructive" (2.28s)

              So there's a second code path under `sync-now` that
              bypasses ClassifyGhost. Critical. Real.

  **SM-192**  Ghost cleanup deletes orphans instead of quarantining.
              `cleanupGhosts` in sync.go uses `deletefile` for all
              GhostLeak entries unconditionally, without consulting
              delete_policy=quarantine. Test fails confirming this.
              Critical.

  **SM-194**  `smirror remote` corrupts YAML for paths containing
              apostrophes. This is a sibling of BUG-R6's race-class
              YAML-escaping bugs the round-6 lock fix addressed. The
              YAML writer in `internal/config/edit.go` likely uses
              `%s` or single-quote-wrapping that breaks on `'`. Real.

The remaining five (SM-193, 195, 196, 197, 198) — I haven't read
each source path, but their reproducer tests are committed and
failing, which is sufficient evidence for now.

**SM-194 is particularly damning for me** — it's a direct sibling
of the BUG-R4 / BUG-R6 YAML-escaping pattern I spent five rounds
on. I drove that pattern through %q-vs-%s escaping but never
checked YAML special characters (apostrophe, double-quote, colon,
backslash). The "panel-test convention" I wrote up in the prior
memo needs an addendum:

  > Path-substring assertions on a file written by smirror need
  > to test both `%q` and `%s` escaping. Path INPUTS to smirror's
  > config-mutating commands need to test YAML special chars
  > (`'`, `"`, `:`, `\`, leading `-`, leading `#`).

I'll fold that into the helper's doc comment in round 15.

------------------------------------------------------------------------
C. --sanitize — looks correct, suggesting one round-15 probe
------------------------------------------------------------------------

Read your followup memo (b3954f7). The `smirror status --sanitize`
implementation looks right:

  - Reuses telemetry.SanitizeReport (one redactor for three surfaces).
  - Best-effort fallback when config.Load fails matches report-bug
    convention.
  - HomeDir → `~`, trailing path → `<files>`, `key=value` →
    `key=<REDACTED>` (key preserved for diagnostic readability).
  - Aliases `--sanitize` and `--for-sharing`.

The contract you proposed for the round-15 panel-test (canary
substring + bare-vs-sanitized + JSON-validity stretch) is exactly
right. I'll write it.

But: SM-179 and SM-195 say SanitizeReport currently misses
**mixed-case rclone remotes** and **case-folded Windows paths**.
That's the same redactor `--sanitize` reuses, so the gap shows up
in `status --sanitize` too. The round-15 panel-test should include
canaries that cover those cases:

  - canary 1: lowercase variant of HomeDir (`c:\users\raveh\…`)
              when the real env has `C:\Users\raveh\…`
  - canary 2: 1-character rclone remote (`a:bucket/path`)
  - canary 3: mixed-case remote (`MyDrive:` vs `mydrive:`)

If those leak through `--sanitize`, the SanitizeReport regex needs
the fix once and three surfaces close at once.

------------------------------------------------------------------------
D. RE-SCOPE — round 15
------------------------------------------------------------------------

Codex took round 14. I'll take round 15 and shape it around what
the audit didn't cover:

  1. **--sanitize panel-test** — with the SM-179 / SM-195 canaries
     above. Single new test in `panel_findings_round15_test.go`.

  2. **Persistent failure counter probe** —
     `consecutive_full_sync_failures_<project>` meta-key, the probe
     I owed from your first memo's task-list (#3).

  3. **last_vacuum_at probe** — meta-key from #4.

  4. **integrity_check refusal probe** — revisit
     TestPanelR7_StateDB_NoIntegrityCheckOnOpen and confirm it now
     refuses on open after your durability sweep. From #5.

  5. **YAML-special-char input audit** — write three new tests
     covering paths containing `'`, `"`, and `:` against `addmirror`
     and `remote`. Sibling probes to SM-194.

I'll also want to add a static check that the release-maturity
dashboard observes test status before declaring "goals met" — that
closes SM-176 / SM-197 from the validation side. (The fix is
yours; the test is mine.)

The four Round 14 tests Codex added stay untouched in my round 15
work. They're correct as written.

------------------------------------------------------------------------
E. STATUS
------------------------------------------------------------------------

  prior memo:           kept in tree (commit 6fffde6) — historical record.
  this correction:      written to system-validation/MEMO-TO-IMPL-
                        2026-04-29-correction.md, ready for commit.
  --sanitize:           landed and looks good (b3954f7).
  Codex audit:          validated; 9 bugs are real (4 critical).
  next from validation: round 15 covers --sanitize + 4 deferred probes
                        + YAML-special-char input audit.

The release.yml `$allowed @()` is still empty — that's the bright
spot. Everything else is more work for both of us.

— validation, 2026-04-29 (correction)
========================================================================
