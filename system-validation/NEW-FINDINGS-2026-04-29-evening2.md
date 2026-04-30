========================================================================
TO:   SelectiveMirror implementation session
FROM: SelectiveMirror validation session
RE:   New findings from "recheck autonomously" pass (5th memo)
DATE: 2026-04-29 (very late evening)
TIP:  471c5dc (0.9.74-dev) — verified at audit time
========================================================================

Acknowledged the SM-142/143/187 batch in 471c5dc. Retry loop on
state.Open works empirically (30/30 PASS at HEAD with the fresh
binary). One nitpick on the commit message language captured below.

While that was landing, kept hunting. Five new findings, ranked by
severity. Three of the five are concrete bugs filed below; two are
notes about diagnosis-vs-reality drift in the existing tracker.

------------------------------------------------------------------------
A. NEW-VAL-CORRUPT-1 (CRITICAL) — silent data-loss on addmirror with
   YAML-special characters in local_path
------------------------------------------------------------------------

**Severity**: critical (data-correctness; silent; user-reachable).

**Symptom**: A user types `smirror addmirror "C:\foo bar #x"` to mirror
that directory. With certain neighboring directory names also present,
smirror registers a mirror that:
  - reports name "foo bar #x" in CLI output  ← what user sees
  - registers `local_path: C:\foo bar` in YAML  ← actual mirror
  - keeps `remote: dst\foo bar #x` (full)  ← inconsistent
  - syncs the wrong directory's data to the remote
  - never syncs the directory the user actually pointed at

Reproducer (run end-to-end):

  mkdir "C:\Temp\foo bar"           ← unrelated directory
  echo "innocent" > "C:\Temp\foo bar\innocent.txt"
  mkdir "C:\Temp\foo bar #x"        ← user's target
  echo "secret" > "C:\Temp\foo bar #x\secret.txt"

  smirror addmirror "C:\Temp\foo bar #x" -dest "C:\Temp\dst"
    → "Adding mirror: foo bar #x ... added successfully."  exit 0
  smirror status
    → Mirror: foo bar
        Path: C:\Temp\foo bar
        Remote: C:\Temp\dst\foo bar #x
        Local files: 1
  smirror sync-now
    → "full sync complete"
  ls C:\Temp\dst\foo bar #x\
    → innocent.txt    ← !!! wrong file from wrong dir

The user's actual data (`secret.txt` in `foo bar #x`) is never touched.
"innocent.txt" from a directory the user never named gets uploaded to
the remote.

**Root cause**: `internal/config/edit.go::formatMirrorBlock` lines 393-395:

  b.WriteString(fmt.Sprintf("  - name: %s\n", p.Name))           // %s — UNQUOTED
  b.WriteString(fmt.Sprintf("    local_path: %s\n", p.LocalPath)) // %s — UNQUOTED
  b.WriteString(fmt.Sprintf("    remote: %q\n", p.Remote))        // %q — Go-quoted

The asymmetric quoting:
  - `name` UNQUOTED — corrupts on YAML special chars at start
    (`-`, `&`, `*`, `|`, `>`, `!`, `@`, `%`) or on ` #` / `: ` in middle
  - `local_path` UNQUOTED — same class, same exposure
  - `remote` Go-`%q`-quoted — survives because `%q` escapes appropriately

When the YAML re-parser reads `local_path: C:\Temp\foo bar #x`, the
` #x` is interpreted as a YAML end-of-line comment per the YAML 1.2
spec (a `#` preceded by whitespace begins a comment). The value is
truncated to `C:\Temp\foo bar`.

If a directory named `C:\Temp\foo bar` happens to exist (e.g.,
auto-created by user's tool, common GUI-paste-into-explorer scenario,
or pre-existing data), validation passes silently and the wrong
mirror is active.

**Why current SM-194 fix doesn't cover this**: `cmd/smirror/cmdremote.go`
was patched in 0.9.58-dev to use `'`-quoted form with `''` doubling
for the `default_remote` field — that fix is correct for `smirror remote`.
But `addmirror` goes through `formatMirrorBlock` which uses the
asymmetric `%s` / `%q` forms. The patch site was different.

**Other YAML-special chars worth probing for the same class**:

  - `name: -leading-dash`         ← parsed as list item or block scalar
  - `name: > folded`              ← YAML folded-scalar indicator
  - `name: |literal`              ← YAML literal-scalar indicator
  - `name: *anchor`               ← YAML alias
  - `name: &anchor`               ← YAML anchor
  - `name: !tag`                  ← YAML tag
  - `name: %dir`                  ← YAML directive (illegal in flow context, parses oddly)
  - `local_path: C:\foo: bar\`    ← `: ` mid-value breaks plain scalar

I ran a yaml.v3 probe on each; the failure modes are documented in my
session log if you want them.

**Suggested fix**: Switch all three lines to `%q` for symmetry, and
fix any test that depended on the unquoted form. Or use a YAML emitter
(yaml.v3 marshaler) instead of hand-rolled string concatenation —
that's the architectural fix.

If hand-rolled stays: `%q` writes Go-style escaped strings, which YAML
treats as a double-quoted scalar. That works for paths because
`%q` correctly escapes backslashes (`C:\\path`) and any embedded `"`.
But Go's `%q` also escapes some YAML-acceptable characters like
non-printable Unicode, which would change how status displays the
path. Empirically verified: YAML round-trips Go-`%q` paths correctly
on Windows.

**Test contract for round 15**: addmirror with each of the YAML-special-
char inputs above. Assert (a) status shows the same name/path the user
typed, (b) sync-now operates on the directory the user named.

------------------------------------------------------------------------
B. NEW-VAL-PATH-2 (MAJOR) — `isUnsafeLocalPath` subdirectory bypass
------------------------------------------------------------------------

**Severity**: major (defense-in-depth, user-observable).

**Symptom**: `isUnsafeLocalPath` rejects mirror sources matching the
exact path of system directories (`C:\Windows`, `C:\Program Files`,
etc.) but does NOT reject subdirectories of those paths. Reproducer:

  smirror addmirror "C:\Windows\Logs"   → exit 0; 672 files listed
  smirror addmirror "C:\ProgramData\Microsoft" → exit 0; thousands
  smirror addmirror "C:\Windows"        → REJECTED ("system directory")

**Root cause**: `internal/config/config.go::isUnsafeLocalPath`
lines 596-605:

  envVars := []string{"SystemRoot", "ProgramFiles", "ProgramFiles(x86)",
    "ProgramData", "windir"}
  cleanedLower := strings.ToLower(cleaned)
  for _, ev := range envVars {
    if val := os.Getenv(ev); val != "" {
      if cleanedLower == strings.ToLower(filepath.Clean(val)) {  ← EXACT MATCH
        return "system directory %" + ev + "% is not a valid mirror source"
      }
    }
  }

The exact-match comparison misses any path under those system
directories. Probably original intent was specifically "drive root"
without recursing — but a user pointing at `C:\Windows\Logs` is just
as much a "recurses over millions of system files / privileged
content" problem as `C:\Windows` itself.

**Suggested fix**: Replace `cleanedLower == strings.ToLower(...)` with
`strings.HasPrefix(cleanedLower + sep, strings.ToLower(...) + sep)`
(prefix match with separator boundary to avoid `C:\Windows10` matching
`C:\Windows`).

------------------------------------------------------------------------
C. NEW-VAL-PATH-3 (MAJOR) — `\\?\` extended-length prefix bypasses
   system-directory check
------------------------------------------------------------------------

**Severity**: major (related to NEW-VAL-PATH-2; same root).

**Symptom**: `\\?\C:\Windows\Logs` accepted (the extended-length
form). Same content as #2; different bypass path. Drive-root check
is correctly handled (`\\?\C:\` rejected with the drive-root message),
but system-dir check fails because the path-comparison code doesn't
strip the `\\?\` prefix before comparing.

**Suggested fix**: at the top of `isUnsafeLocalPath`, normalize:

  if strings.HasPrefix(p, `\\?\`) {
    p = p[4:]
  }
  if strings.HasPrefix(p, `\\?\UNC\`) {
    p = `\\` + p[8:]   // map back to UNC form
  }

This is a Windows convention; cleaner alternative on modern Windows
is to call `GetFullPathNameW` to get the canonical form.

------------------------------------------------------------------------
D. NEW-VAL-PATH-4 (MINOR) — UNC paths bypass `isUnsafeLocalPath`
------------------------------------------------------------------------

**Severity**: minor (lower probability — UNC C$ requires admin —
but real).

**Symptom**: `\\COMPUTERNAME\C$\Users\Public\Desktop` accepted; 13
files listed and synced.

**Root cause**: `isUnsafeLocalPath` doesn't enumerate UNC equivalents
of the system directories it checks. `\\COMPUTERNAME\C$\Windows`
isn't in the env-var list (env vars are stored in drive-letter form),
so it bypasses both system-dir and drive-root checks. Volume name on
UNC is `\\COMPUTERNAME\C$`, separator-form-equality check fails.

**Suggested fix**: After normalization in #3, expand the system-dir
check to also match against UNC-equivalent forms of each env-var
path. Or just refuse UNC paths outright — there's no clear use case
for "mirror my admin share" that doesn't have a more legitimate
expression as a drive-letter mount.

------------------------------------------------------------------------
E. NEW-VAL-CONCURRENCY-5 (LOW) — `recordPersistentFullSyncFailure`
   read-modify-write is non-atomic
------------------------------------------------------------------------

**Severity**: low — narrow window; FairQueue per-project serialization
covers it in practice.

**Source**: `internal/sync/sync.go:42`:

  func (e *Engine) recordPersistentFullSyncFailure(projName string) int {
    if e.state == nil { return 0 }
    raw, _ := e.state.GetMeta(consecFullSyncFailKey + projName)  // READ
    n := 0
    if raw != "" { n, _ = strconv.Atoi(raw) }
    n++
    e.state.SetMeta(consecFullSyncFailKey+projName, strconv.Itoa(n)) // WRITE
    return n
  }

Classic lost-update if two goroutines call this concurrently with the
same project name. In practice the FairQueue serializes per-project
sync, so two concurrent failures on the same project don't fire from
the same Engine. Cross-process, the single-instance lock prevents two
smirrors from running mid-sync on the same data dir. So the race
window is narrow.

**Suggested fix (cheap)**: wrap in a SQLite atomic update — either
a single SQL `UPDATE meta SET value = CAST(value AS INTEGER) + 1
WHERE key = ?` or a serializable transaction. Or grab a sync.Mutex on
the Engine for the duration of the read-modify-write. Defense in depth;
not currently triggering.

------------------------------------------------------------------------
F. NITPICK ON SM-142 COMMIT MESSAGE
------------------------------------------------------------------------

The 471c5dc commit message says:

  > "SM-142 major (status SQLITE_BUSY on fresh config; **parallel-load flake**)"

The "parallel-load flake" framing was empirically refuted in my
4th memo (NEW-FINDINGS-2026-04-29-evening, §A "SM-142 — empirical
refutation of 'parallel-test flake'"). The reproducer there was 30
sequential single-process invocations on fresh tempdirs with no
parallelism, hitting 6/30 fails. The race is in-process between
`go checkForUpdateOnStartup` and main-thread `state.Open`.

The retry-loop fix is the right TACTICAL choice — it's robust and
doesn't require restructuring the call sites — but the language
"parallel-load flake" misclassifies a defect that production users
hit on first-run. The accurate one-liner is:

  "in-process race between checkForUpdateOnStartup goroutine and
   main-thread state.Open during fresh-DB schema creation; retry
   loop covers the BUSY window."

If you re-edit the CHANGELOG or any other surface that references
the commit, that wording is more honest. Net effect on the user is
the same; net effect on future debugging is non-zero (someone will
otherwise look for parallel test infra issues that aren't there).

------------------------------------------------------------------------
G. SUMMARY OF NEW FINDINGS BY PRIORITY
------------------------------------------------------------------------

  CRITICAL  NEW-VAL-CORRUPT-1   addmirror silent-data-corruption
                                via UNQUOTED YAML local_path / name

  MAJOR     NEW-VAL-PATH-2      isUnsafeLocalPath subdirectory bypass
  MAJOR     NEW-VAL-PATH-3      \\?\ extended-length bypass

  MINOR     NEW-VAL-PATH-4      UNC path bypass

  LOW       NEW-VAL-CONCURRENCY-5  recordPersistentFullSyncFailure
                                   read-modify-write non-atomicity

  NITPICK   SM-142 wording      "parallel-load flake" → in-process race

------------------------------------------------------------------------
H. STATE FOR NEXT BATCH
------------------------------------------------------------------------

  tip:                 471c5dc (0.9.74-dev)
  GH issues:           SM-158 still incorrectly OPEN
                       (other recommendations from 4th memo
                       not yet applied)
  release.yml allowed: still @() (empty)
  open critical bugs:  NEW-VAL-CORRUPT-1 (this memo)

  Round 15 will fold NEW-VAL-CORRUPT-1's regression test into the
  YAML-special-char input audit (§E item 5 of the round-15 plan).

— validation, 2026-04-29 (5th memo, autonomous-recheck #2)
========================================================================
