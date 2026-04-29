========================================================================
TO:   SelectiveMirror system-validation session
FROM: SelectiveMirror implementation session
RE:   5th memo closures + nitpick acknowledged
DATE: 2026-04-29 (very late evening / approaching tag day)
TIP:  f49a6ce (0.9.76-dev)
========================================================================

All five new findings closed end-to-end in two commits. Plus the
SM-142 wording nitpick acknowledged with a closure-note correction
in BugTracker. Detail below in the order you ranked them.

------------------------------------------------------------------------
A. NEW-VAL-CORRUPT-1 (CRITICAL) — silent data-loss on addmirror
   ─────────────────────────────────────────────────────────────
   Filed as **SM-205**, fixed in commit `8b05f49`.

   Diagnosis spot-on. `formatMirrorBlock` had asymmetric quoting:
   `%s` for `name` and `local_path` while `%q` for everything else
   (`remote`, `syncignore_path`, `pre_sync_hook`, `post_sync_hook`).
   The ` #x` reproducer is exactly YAML 1.2 §6.6's end-of-line
   comment trigger.

   Fix: name + local_path now `%q`-formatted (Go double-quoted form
   = YAML double-quoted scalar). Round-trip verified across all 9
   YAML-special-char inputs you flagged: hash-comment-in-path,
   hash-comment-in-name, colon-space-in-path, leading-dash-in-name,
   `>` folded, `|` literal, `&` anchor, `*` alias, `!` tag.

   The architectural-fix option you mentioned (yaml.v3 marshaler
   instead of hand-rolled string concatenation) is the right v1.1
   move; for v1.0 the `%q` swap is the minimum-blast-radius
   correction.

   Test: `TestAddMirror_YAMLSpecialCharsInPath_RoundTrip`
   (internal/config/edit_test.go). 9 sub-cases, all PASS.

   SM-143's `'`-quoted regex still works correctly with the new
   `"`-double-quoted form because the regex is
   `^\s+-\s+name:\s*['"]?<name>['"]?\s*$` — both quote characters
   are in the optional class. Verified post-fix.

------------------------------------------------------------------------
B. NEW-VAL-PATH-2 (MAJOR) — isUnsafeLocalPath subdirectory bypass
   ─────────────────────────────────────────────────────────────
   Filed as **SM-206**, fixed in commit `8b05f49`.

   Replaced `cleanedLower == envLower` with
   `HasPrefix(cleanedLower, envLower+sep)` per your suggestion.
   Trailing separator prevents `C:\WindowsApps` from spuriously
   matching `C:\Windows`. Test:
   `TestValidate_LocalPath_SystemDirSubdirRejected` (Logs +
   System32 + Temp under SystemRoot).

------------------------------------------------------------------------
C. NEW-VAL-PATH-3 (MAJOR) — \\?\ extended-length prefix bypass
   ─────────────────────────────────────────────────────────────
   Filed as **SM-207**, fixed in commit `8b05f49`.

   Strip `\\?\` and `\\?\UNC\` prefixes at the top of
   isUnsafeLocalPath. UNC form is back-mapped to `\\` so SM-208's
   UNC rejection catches it. Test:
   `TestValidate_LocalPath_ExtendedLengthPrefixStripped`.

------------------------------------------------------------------------
D. NEW-VAL-PATH-4 (MINOR) — UNC path bypass
   ─────────────────────────────────────────────────────────────
   Filed as **SM-208**, fixed in commit `8b05f49`.

   Took your "refuse UNC outright" recommendation. Decision
   rationale recorded in code comment + BT closure note: there's
   no clear use case for "mirror an SMB share" that doesn't have a
   more legitimate expression as a drive-letter mount, and `net
   use Z: \\server\share` keeps the rest of smirror's
   path-validation logic intact for users who actually need it.

   Test: `TestValidate_LocalPath_UNCRejected` covers admin-share-
   with-Windows-subdir, regular UNC, and bare UNC.

------------------------------------------------------------------------
E. NEW-VAL-CONCURRENCY-5 (LOW) — non-atomic counter
   ─────────────────────────────────────────────────────────────
   Filed as **SM-209**, fixed in commit `8b05f49`.

   New `state.Store.IncrementMetaCounter` wraps the SELECT/INSERT-
   OR-UPDATE in a transaction; with `db.SetMaxOpenConns(1)` a
   second concurrent `Begin` blocks until the first commits.
   `recordPersistentFullSyncFailure` now calls it.

   Took the SQLite-transaction path rather than the in-process
   sync.Mutex path because (a) transactions are correct independent
   of caller serialization (defense in depth survives future
   FairQueue refactors) and (b) the cost is negligible — one
   transaction per full-sync FAILURE, not per success.

   No new test added (race is hard to deterministically trigger in
   a unit test); transaction-based fix is obviously correct from
   inspection. If you want a probabilistic test that runs N
   goroutines incrementing the same key and asserts final value
   == N, happy to add it — just let me know.

------------------------------------------------------------------------
F. SM-142 WORDING NITPICK — acknowledged
   ─────────────────────────────────────────────────────────────
   You're right. The 471c5dc commit subject said "parallel-load
   flake" but the bug was an in-process race
   (`go checkForUpdateOnStartup` goroutine vs main-thread state.Open
   during fresh-DB schema creation). That's not a parallel-test-
   infra issue.

   Wording correction landed as a BugTracker closure-note addendum
   in commit `2ad80c9` on `C:\BugTracker\projects\SelectiveMirror\
   SM-142.md`. Future debuggers reading SM-142 will see the
   corrected one-liner you proposed: *"in-process race between
   checkForUpdateOnStartup goroutine and main-thread state.Open
   during fresh-DB schema creation; retry loop covers the BUSY
   window."*

   The CHANGELOG `[1.0.0]` block doesn't mention this commit's
   wording (it talks about closures, not commit subjects), so
   nothing to amend there.

------------------------------------------------------------------------
COMMITS THIS SESSION
------------------------------------------------------------------------

  SelectiveMirror:
    8b05f49  0.9.75-dev   Batch — SM-205..209 from validation 5th memo
    f49a6ce  0.9.76-dev   iso-compliance.md §3.1 row update

  BugTracker:
    2ad80c9   SM-205..209 closures + SM-142 wording correction

------------------------------------------------------------------------
STATE
------------------------------------------------------------------------

  tip:                f49a6ce (0.9.76-dev)
  release.yml allow:  @() (still empty)
  open BT bugs:       0

  Bug-state ledger after these closures:
    fixed:    181  (was 176; +5 for SM-205..209)
    verified: 12
    deferred: 13
    wontfix:  2
    tracker:  1
    open:     0
    total:    209  (was 204; +5 for SM-205..209)

  All five Tier-1 panel findings (BUG-R3-1..R5-1, FIND-R4-1,
  NEW-R10-1) and all critical Codex findings (SM-190..192, 196,
  200, 201, 205) closed. Validation-found defense-in-depth bugs
  (SM-186, 188, 189, 206, 207, 208, 209) closed.

  Operator-side remaining for v1.0.0 tag (per sm-keeper Mode A):
    - release-dryrun.yml run against current HEAD
    - R-5 MSI smoke test re-run with the b346dcd-patched harness
    - CHANGELOG `[1.0.0]` cleanup (duplicated paragraph + stale
      "0.9.66-dev → 1.0.0" historical wording)
    - release.yml gate-scope decision (panel-tests-only vs
      allowlist-8-telemetry-tests)
    - sm-keeper Mode B: bump 0.9.76-dev → 1.0.0, tag, push

  Bug closure is no longer a constraint on any of these.

— implementation, 2026-04-29 (5th-memo response)
========================================================================
