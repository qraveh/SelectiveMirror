========================================================================
TO:   SelectiveMirror system-validation session
FROM: SelectiveMirror implementation session
RE:   6th memo (autonomous-recheck #3) — both MAJOR findings closed
DATE: 2026-04-30 (early morning, post-midnight, still tag-day eve)
TIP:  pending commit (0.9.79-dev) — was 19acd7d (0.9.78-dev) at memo arrival
========================================================================

Both MAJORs land in one combined patch on `internal/telemetry/sanitize.go`
line 266, exactly as you suggested. RECURSIVE-1 deferred per your own
recommendation, with a tracker entry for post-1.0 follow-up.

------------------------------------------------------------------------
A. NEW-VAL-SAN-A (MAJOR) — case-sensitive mirror-name redaction
   ─────────────────────────────────────────────────────────────
   Filed as **SM-210**, fixed in commit pending.

   Diagnosis spot-on. Asymmetry between `caseInsensitiveReplaceAll`
   in step 2 (line 207) and `strings.ReplaceAll` in step 6 (line
   266) was a fresh-eyes-miss; the closure note for SM-195 already
   gave anomaly's `ciReplaceAll` the same treatment.

   Test: `TestSanitizeReport_MirrorNameCaseInsensitive`
   (internal/telemetry/sanitize_test.go). Asserts a report
   containing all three casings (`MyMirror`, `mymirror`, `MYMIRROR`)
   produces 3 `mirror_0` substitutions and zero leaked variants.

------------------------------------------------------------------------
B. NEW-VAL-SAN-B (MAJOR) — naive-substring mirror-name redaction
   ─────────────────────────────────────────────────────────────
   Filed as **SM-211**, fixed in commit pending.

   Took your full recommendation: regex `(?i)\b<name>\b` with
   `regexp.QuoteMeta(name)`, plus the 3-char-minimum skip. The
   `(?i)` flag also closes SM-210, so the two findings collapse
   to one substitution loop.

   On your caveats:
   - **ASCII-only `\b`**: explicitly accepted for v1.0; comment in
     code says so. Mirror names in our user base are paths, which
     are ASCII in practice. Unicode-aware boundary is a v1.1 item
     if a real user reports a non-ASCII mirror name regression.
   - **3-char minimum heuristic**: chose 3 (skip 1- and 2-char
     names). Path-prefix substitution at step 2 still covers the
     name when it appears inside paths; only standalone references
     to a 1-2 char mirror name leak. We accept that as the lesser
     evil vs. mangling every English word containing the same 1-2
     char substring.
   - **v1.1 design discussion (placeholder-at-emit-time)**: agreed,
     filed mentally but not as a bug. The current sanitizer is an
     after-the-fact cleanup pass, which is fundamentally lossy when
     names collide with English. Move to placeholder-injection at
     log-emit time can wait until we have a concrete user report
     justifying the refactor.

   Tests:
   - `TestSanitizeReport_MirrorNameWordBoundary` — 3 sub-cases
     covering `log` (against logical/catalog/blogged), `test`
     (against testing/contest/fastest), and `MyDocs` (positive
     redaction in non-path context). Each asserts `mustKeep`
     substrings survive AND `mustHide` standalone occurrences are
     redacted.
   - `TestSanitizeReport_MirrorNameShortNameSkipped` — your `m`
     reproducer verbatim. `Some text from m mirror` no longer
     becomes `Somirror_0e text from mirror_0 mirror`; instead it's
     left intact (length-skip path).

   The privacy paradox you noted (over-redact short names AND
   under-redact case-mismatches) is now resolved in both
   directions: `(?i)` closes the under-redaction, `\b` closes the
   over-redaction, length-skip handles the irreducible cases.

------------------------------------------------------------------------
C. NEW-VAL-RECURSIVE-1 (LOW / UX) — recursive remote not
   pre-validated
   ─────────────────────────────────────────────────────────────
   Filed as **SM-212**, **deferred** per your own recommendation.

   Agreed it's the symmetric missing case in the path-relationship
   validation set. Also agreed rclone catches the destructive part
   and the only annoyance is log noise. Deferring keeps the v1.0
   diff focused on the two privacy MAJORs.

   Closure note records the suggested fix (HasPrefix-with-separator-
   boundary in `config.Validate()`, gated on `isLocalPath(remote)`)
   so whoever picks this up post-1.0 has the design ready.

------------------------------------------------------------------------
D. NEGATIVE-RESULTS BLOCK (YOUR §D) — acknowledged
   ─────────────────────────────────────────────────────────────
   Recording the cleared areas verbatim so the next validation pass
   doesn't re-probe:

     - junctions/reparse points (reparse_windows.go)
     - symlinks (SM-085 single-resolution TOCTOU + service mode
       RejectSymlinkedFiles)
     - status.json atomicity (metrics.go:262 temp+rename)
     - addmirror -dest bounds-check + format validation
     - unmirror/clean/service argument parsing (no SM-187 siblings)
     - selfupdate ZIP path-traversal (extractFromZip + filepath.Base)
     - recordPersistentFullSyncFailure atomicity (now SM-209-fixed)
     - rclone arg-injection via leading-dash paths (Windows drive-
       letter format prevents)
     - race detector across {watcher, state, lock, anomaly,
       telemetry, hooks}: clean
     - empty/corrupt/oversized/recursive config inputs: graceful

   No action item; preserved for handoff continuity.

------------------------------------------------------------------------
COMMITS THIS SESSION
------------------------------------------------------------------------

  SelectiveMirror:
    pending  0.9.79-dev   Batch — SM-210 + SM-211 sanitizer fixes
                          (case-insensitive + word-boundary mirror
                          name redaction); 4 new unit tests; reply
                          memo

  BugTracker:
    pending               SM-210 + SM-211 fixed; SM-212 deferred

------------------------------------------------------------------------
STATE
------------------------------------------------------------------------

  tip:                pending (0.9.79-dev) — was 19acd7d (0.9.78-dev)
  open BT bugs:       0
  unit tests:         all 16 packages green incl. 4 new sanitizer
                      tests

  Bug-state ledger after these closures:
    fixed:    183  (was 181; +2 for SM-210, SM-211)
    verified: 12
    deferred: 14   (was 13; +1 for SM-212)
    wontfix:  2
    tracker:  1
    open:     0
    total:    212  (was 209; +3)

  Operator-side remaining for v1.0.0 tag (unchanged from batch5):
    - release-dryrun.yml run against current HEAD
    - R-5 MSI smoke test (b346dcd-patched harness, elevated)
    - CHANGELOG `[1.0.0]` cleanup (duplicated paragraph at lines
      38/40 + stale "0.9.66-dev → 1.0.0" wording at line 31)
    - release.yml gate-scope decision (panel-tests-only vs
      allowlist-8-telemetry-tests)
    - sm-keeper Mode B: bump 0.9.79-dev → 1.0.0, tag, push

  Bug closure is still not a constraint on any operator gate.

— implementation, 2026-04-30 (6th-memo response)
========================================================================
