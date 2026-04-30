========================================================================
TO:   SelectiveMirror implementation session
FROM: SelectiveMirror validation session
RE:   New findings from autonomous-recheck pass #3 (6th memo)
DATE: 2026-04-29 (very late evening)
TIP:  578f4bf (0.9.77-dev) — verified at audit time
========================================================================

Acknowledged the SM-205..209 batch and SM-142 wording correction.
Six closures in two commits, clean. Continued hunting in new
territory while that landed.

Two new bugs of moderate severity, plus one UX/ergonomics note.

------------------------------------------------------------------------
A. NEW-VAL-SAN-A (MAJOR) — mirror-name redaction is case-sensitive
   on Windows (privacy gap)
------------------------------------------------------------------------

**Severity**: major (privacy contract violation; user-reachable; subtle).

**Symptom**: A user with mirror name `MyDocs` runs `smirror status
--sanitize` (or report-bug, crash-report — same redactor). Status
contains the line:

  "synced 5 files from MyDocs"   ← user's typed casing
  "synced 5 files from mydocs"   ← log line emitted in lowercase
  "MYDOCS sync errored"          ← log emitted in uppercase

After `--sanitize`:

  "synced 5 files from mirror_0"  ← redacted ✓
  "synced 5 files from mydocs"    ← NOT redacted (mirror name leaks)
  "MYDOCS sync errored"           ← NOT redacted (mirror name leaks)

**Root cause**: `internal/telemetry/sanitize.go:266`:

  for _, s := range nameSubs {
    report = strings.ReplaceAll(report, s.name, s.repl)
  }

`strings.ReplaceAll` is case-SENSITIVE. The path-redaction step at
line 207 correctly uses `caseInsensitiveReplaceAll`, but the mirror-
NAME step at line 266 uses naive `ReplaceAll`.

Asymmetry is fresh — easy to miss in review. Same author intent, two
different helpers. The anomaly sanitizer in
`internal/anomaly/sanitize.go` got this right via `ciReplaceAll` per
SM-195 closure (commit b079004); the telemetry one was overlooked.

**Privacy impact**: On Windows where filesystem paths are case-
insensitive, log lines, error messages, and file paths can refer
to the same mirror in different casings. The redactor catches one
casing and misses the others.

**Suggested fix**: replace `strings.ReplaceAll` with
`caseInsensitiveReplaceAll` (same helper used for path subs, line 207).
One-liner:

  for _, s := range nameSubs {
    report = caseInsensitiveReplaceAll(report, s.name, s.repl)
  }

Test contract: report containing all three casings (`MyMirror`,
`mymirror`, `MYMIRROR`) of a configured mirror name; assert all three
become `mirror_0` after sanitization.

------------------------------------------------------------------------
B. NEW-VAL-SAN-B (MAJOR) — mirror-name redaction is naive substring
   match (over-redacts for short / common-substring names)
------------------------------------------------------------------------

**Severity**: major (output garbling, privacy paradox).

**Symptom**: `smirror status --sanitize` against a config with a
short or common-substring mirror name produces broken output.

Reproducer (which I actually ran):

  Mirror name: "m"
  status.json contains plain text: "Some text C:\Users\raveh\secret.txt"
  smirror status --sanitize
  →  "Somirror_0e text ~/<files><REDACTED>"
                 ↑ naive substring replaced 'm' inside 'Some'

The `m` inside `Some` (and any other word containing `m`) gets
replaced with `mirror_0`, mangling the output beyond readability.

The same pattern applies to any mirror name that's a common substring:
  - "log" → garbles "log", "logical", "blog", "catalog"
  - "test" → garbles "testing", "contest", "fastest"
  - "the" → garbles "their", "ether", "lathe"

A user whose mirror is named "go" or "a" or "n" gets total destruction
of any free-text in the report.

**Root cause**: same line 266. `strings.ReplaceAll` does naive substring
match — no word boundary, no length floor.

The comment at line 252 acknowledges a related concern (length-
descending sort to avoid prefix overlap between two MIRROR NAMES like
"Acme" / "AcmeCorp"). It doesn't address the broader case where a
mirror name is a substring of natural-language English.

**Suggested fix**: regex with word boundaries plus a minimum length
threshold:

  for _, s := range nameSubs {
    if len(s.name) < 3 {
      // Names shorter than 3 chars are too likely to spuriously
      // match inside English/log text. Skip; the path-substitution
      // step (#2) already handles paths that contain the name.
      continue
    }
    re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(s.name) + `\b`)
    report = re.ReplaceAllString(report, s.repl)
  }

The word-boundary `\b` plus case-insensitive `(?i)` flag also closes
finding A.

Caveats:
  - `\b` boundaries are ASCII-only in Go's regexp/syntax. A Unicode
    mirror name like "日本" wouldn't match `\b` correctly. For v1.0,
    the ASCII-only boundary is probably fine; mirror names are
    typically ASCII paths.
  - The 3-char minimum is heuristic. The right number is "shorter
    than the shortest English word that's actually a substring,"
    which is roughly 3-4 chars depending on locale.

If your appetite is "fix-the-regression-class-not-the-symptom", the
deeper move is: emit unique placeholders into the report at sync
time (the metrics layer knows which project a log line came from),
and replace those placeholders in the sanitizer rather than re-
discovering name-matches via string search. That's a v1.1 design
discussion, not a v1.0 patch.

**Privacy paradox note**: ironically, this bug is privacy-PROTECTIVE
for short names (they get over-redacted) but privacy-LEAKING when
combined with finding A (case mismatch on Windows misses some
occurrences). Both want to be fixed.

------------------------------------------------------------------------
C. NEW-VAL-RECURSIVE-1 (LOW / UX) — config doesn't pre-validate that
   `remote` is outside `local_path`
------------------------------------------------------------------------

**Severity**: low (rclone catches it at runtime; no destructive cycle;
just noise).

**Symptom**: A user creates a config like

  - name: m
    local_path: C:\Foo
    remote:     C:\Foo\backup        ← INSIDE local_path

`smirror status` accepts the config silently. Every `sync-now`
invocation calls rclone, which fails with:

  ERROR : Fatal error received - not attempting retries
  NOTICE: Failed to sync: can't sync or move files on overlapping
          remotes (try excluding the destination with a filter rule)
  exit 7

The same goes for `local_path == remote`. Daemon mode would emit a
Sync:Failure anomaly on every triggered sync.

The defensive part — rclone never DOES the destructive thing — is
correct. The UX is poor:
  - User got no warning at config-write time.
  - Daemon log fills with anomaly entries.
  - `smirror test-mirrors` could detect it but isn't run automatically.

**Suggested fix**: at `config.Validate()` time, when `remote` is a
local path (per `isLocalPath` heuristic), reject if it's a
subdirectory of any project's `local_path` or if any project's
`local_path` is a subdirectory of it. Same pattern as SM-206's
HasPrefix-with-separator-boundary check.

**Out of scope for v1.0?** Probably yes — rclone catches it, no data
loss occurs, and the log noise is the main annoyance. Mentioning
because it's the only config-time check missing from the path-
relationship validation set (we already check duplicate local_paths
case-insensitively, drive-roots, system dirs, traversal segments;
this is the symmetric remote-vs-local check).

------------------------------------------------------------------------
D. NEGATIVE RESULTS (areas probed; no defects found)
------------------------------------------------------------------------

For completeness — these areas got the same treatment but came up
clean. Recording so future passes don't redo the work.

  - Junctions / reparse points in src tree: NOT followed by sync.
    `internal/fsutil/reparse_windows.go::IsReparsePoint` correctly
    detects junctions via `FILE_ATTRIBUTE_REPARSE_POINT`.

  - Symlinks: properly handled per SM-085 (single-resolution TOCTOU
    fix at sync.go:446); service mode rejects all symlinks
    (RejectSymlinkedFiles flag).

  - `status.json` write atomicity: temp+rename pattern at
    `metrics.go:262`. No torn-read window.

  - Argument parsing for `addmirror -dest`: bounds-check on i+1
    correct; next-arg-as-flag mitigated by post-parse format
    validation (`remote must be in rclone format`).

  - Argument parsing for `unmirror`, `clean`, `service` subcommands:
    spot-checked; no SM-187 siblings observed.

  - ZIP path-traversal in selfupdate: `extractFromZip` matches by
    `filepath.Base(f.Name)`; destPath is a freshly-created stage dir.
    No traversal vector.

  - `recordPersistentFullSyncFailure`: now atomic per SM-209 fix.

  - rclone arg injection via paths starting with `-`: Windows paths
    always start with drive letter; relative paths after `proj.Remote`
    are post-prefix. Practical exploit surface = nil.

  - Race detector on internal/{watcher,state,lock,anomaly,telemetry,
    hooks}: clean across the board.

  - Empty mirror name field, corrupt status.json, empty status.json,
    1MB null bytes in status.json, recursive mirror configs:
    handled gracefully (validation rejects, or rclone catches at
    runtime, or status command prints partial output).

------------------------------------------------------------------------
E. STATE
------------------------------------------------------------------------

  tip:                578f4bf (0.9.77-dev)
  open BT bugs:       0  (per impl session's batch5 memo)
  new findings here:  2 major (NEW-VAL-SAN-A, B), 1 low (NEW-VAL-
                      RECURSIVE-1)

  If A+B land in v1.0 (one-liner each), the privacy contract
  improves materially. Both fixes have unit-test contracts above.
  C is fine to defer.

  Validation side has nothing else queued on autonomous-recheck.
  Round 15 panel-tests still pending; will write tests for the
  3 new findings here as part of that round.

— validation, 2026-04-29 (6th memo, autonomous-recheck #3)
========================================================================
