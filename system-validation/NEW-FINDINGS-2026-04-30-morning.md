========================================================================
TO:   SelectiveMirror implementation session
FROM: SelectiveMirror validation session
RE:   New findings from autonomous-recheck pass #4 (7th memo) —
      multi-specialist sweep
DATE: 2026-04-30 (morning)
TIP:  578f4bf (0.9.77-dev) — verified at audit time
========================================================================

Used adversarial / edge-case / architect / reliability / security
specialist lenses for this pass. Most directions came up negative —
recording them so future passes don't redo the work. One major
finding, two defense-in-depth observations.

------------------------------------------------------------------------
A. NEW-VAL-WIN-ACL (MAJOR) — service-mode privacy gap on
   %ProgramData%\SelectiveMirror\
------------------------------------------------------------------------

**Severity**: major (privacy contract violation; affects multi-user
Windows systems running smirror as a service).

**Symptom**: When smirror runs in service mode (LocalSystem) on a
multi-user Windows system, every file the daemon writes to its data
dir is readable by all users on the system. Includes:

  state.db                  → contains all sync history, file paths,
                              error messages with paths
  status.json               → contains LastError with raw paths
                              (intentional per SEC-L4 / `--sanitize`)
  anomalies/*.jsonl         → contains paths in Anomaly.Path,
                              Detail, Hypothesis fields
  early.log                 → contains pid, os.Args, isService flag
  service-crash.log         → contains crash stacks with paths

The developer intent in `cmd/smirror/main.go:81-88` is clearly to
restrict access:

  earlyLogPath := earlyLogTarget(service.IsWindowsService())
  if earlyLogPath != "" {
    _ = os.MkdirAll(filepath.Dir(earlyLogPath), 0700)         ← intent
    earlyLog, _ := os.OpenFile(earlyLogPath, ..., 0600)       ← intent
    ...
  }

The comment block at lines 73-79 says: *"PID + os.Args were readable
by anyone with C:\ list access (which is everyone by default)."* —
acknowledging the privacy concern.

**Root cause**: POSIX file mode bits (0600, 0700, 0755) are SILENTLY
IGNORED on Windows. Go's syscall layer doesn't translate them to
Windows ACLs. Files created via `os.OpenFile` inherit the parent
directory's ACL. On default Windows systems:

  %ProgramData%\               BUILTIN\Users: Read & Execute
  %ProgramData%\SelectiveMirror\  inherits → BUILTIN\Users: Read

So every file in the data dir is world-readable, regardless of the
0600 mode in the source.

**Verification**: `internal/config/acl_windows.go` exists and IS
relevant — but it's an AUDITOR, not a HARDENING WRITER:

  - `IsAdminWritableOnly(path)` — reads existing DACL, walks ACEs,
    refuses to load configs writable by non-admin trustees.
  - Used in service mode to refuse user-writable config (privilege-
    escalation defense — SEC-H6 input-side fix).
  - Does NOT set DACL on data-dir files at write time.

There's NO symmetric `RestrictDataDir(path)` function. Confirmed via
recursive grep:

  grep -rE "DACL|GetSecurityInfo|SetNamedSecurityInfo|sddl|hardenAC"
       --include="*.go" .
  → only matches in acl_windows.go (auditor) and syncnow_windows.go
    (kernel-event DACL — different surface).

**Impact**: On a multi-user Windows server (corporate fileserver,
shared-build agent, RDP host), every standard user can read:

  - Every file path the smirror service has touched
    (sync_log, file_state, anomaly Detail, status.json LastError)
  - Every error message containing paths
  - The pid + command line of the service from early.log

**Why user mode is fine**: User mode (`smirror task install` or
foreground `smirror start`) writes to `%LOCALAPPDATA%`, which IS
user-only by default ACL. Privacy preserved there.

**Suggested fix**: at service-install time and at first-run-creates-
data-dir time, apply a restrictive DACL:

  - SYSTEM: Full
  - Administrators: Full
  - SelectiveMirror service account: Full (modify)
  - (deny / no entry for) Users / Authenticated Users / Everyone

Implementation via `windows.SetNamedSecurityInfo` with a freshly-
constructed DACL containing only the three entries above. WiX option:
add `<util:PermissionEx>` to the data-dir component if MSI ever
creates the dir. Currently MSI doesn't create it; runtime does.

**Test contract for round 15**: in service mode, after one sync
cycle, assert `icacls %ProgramData%\SelectiveMirror` does NOT include
`BUILTIN\Users:(...)` allow ACEs. Standard user (`runas` or AppContainer
test) cannot read state.db.

**Why this passed prior security audits**: the audit-2026-04-18
review focused on input-side privilege escalation (SEC-H6:
`IsAdminWritableOnly` for config). The output-side privacy of the
data dir wasn't on its radar — both sides need symmetric treatment.

------------------------------------------------------------------------
B. NEW-VAL-MIG-ATOMICITY (LOW) — schema migrations not transactional
------------------------------------------------------------------------

**Source**: `internal/state/state.go::runMigrations` lines 285-289:

  for i := currentVersion; i < len(migrations); i++ {
    if err := migrations[i](db); err != nil {
      return fmt.Errorf("migration %d: %w", i, err)
    }
  }

Each migration runs as a separate `db.Exec` call. If migration N+1
fails partway through (DDL succeeds, subsequent UPDATE fails — or
power loss between two DDL statements), the state DB is left in a
mixed state. SQLite auto-commits DDL, so partial CREATE TABLE / ADD
COLUMN persist.

**Recovery behavior**: depends on whether each migration uses
`CREATE TABLE IF NOT EXISTS` and `ADD COLUMN` in idempotent form. If
yes, re-running the failed migration succeeds. If a migration uses
non-idempotent forms (`CREATE TABLE` without IF NOT EXISTS, `ALTER
TABLE … ADD COLUMN` of an already-existing column), the second
attempt fails with a different error than the first.

**Severity**: low. Migrations rarely fail at runtime; the
deterministic-test surface for this is small. Worth noting because
the migration list is GROWING (at len(migrations) > 2 currently per
the schema_version refusal message), and any new migration needs to
be checked for partial-failure recovery.

**Suggested fix**: wrap each migration in a transaction:

  tx, err := db.Begin()
  if err != nil { return err }
  if err := migrations[i](tx); err != nil {
    tx.Rollback()
    return fmt.Errorf("migration %d: %w", i, err)
  }
  // Each migration's body bumps schema_version under the same tx.
  tx.Commit()

Caveat: SQLite supports DDL inside transactions (most other RDBMS
don't), so this is feasible. Existing migrations need no signature
change; the migration func type changes from `func(*sql.DB) error`
to `func(*sql.Tx) error`.

------------------------------------------------------------------------
C. NEW-VAL-VACUUM-EBUSY (LOW) — last_vacuum_at write not checked
------------------------------------------------------------------------

**Source**: `internal/state/state.go::VacuumIfStale` line 333:

  if _, err := s.db.Exec("VACUUM"); err != nil {
    return false, err
  }
  s.SetMeta("last_vacuum_at", time.Now().UTC().Format(time.RFC3339))  ← unchecked
  return true, nil

If `SetMeta` fails (transient DB error, disk full, etc.), the
timestamp isn't persisted. Next call sees an unchanged
`last_vacuum_at` and runs VACUUM again immediately. Could turn into
a tight loop of VACUUM-every-heartbeat under disk pressure (the same
condition that made SetMeta fail).

**Severity**: low. Only manifests under disk-full / state DB
contention. VACUUM itself isn't destructive, just expensive.

**Suggested fix**: check the SetMeta error and either fail closed
(return error) or log and continue. One-liner.

------------------------------------------------------------------------
D. SPECIALIST LENSES — directions probed, all NEGATIVE
------------------------------------------------------------------------

For traceability, recording the directions that came up clean. Future
autonomous passes can skip these unless source materially changes.

  ADVERSARIAL
  ───────────
  - Filename arg-injection into rclone (`--remove-source-files`
    as a filename): not exploitable. Paths always start with drive
    letter on Windows; relative paths get post-prefix in
    `proj.Remote + "/" + relPath`. rclone receives non-flag-shaped
    positional args.
  - Filter regex DoS via `.syncignore`: Go's regexp package uses
    RE2, no catastrophic backtracking by construction.
  - TOCTOU between config.yaml read and write: writers use
    `writePreservingMode` (atomic temp+rename), readers see either
    old-or-new content, never torn.
  - YAML billion-laughs: yaml.v3 has built-in alias-depth limit;
    short-cycle expansion expands at parse time but is bounded.

  EDGE CASE
  ─────────
  - Corrupt state.db (garbage bytes): rejected with
    `"creating schema: file is not a database"`, exit non-zero.
  - State DB at unsupported schema version: rejected with the
    forward-only-migration error from state.go.
  - State DB integrity_check failure on Open: rejected per the
    `PRAGMA integrity_check` gate at state.go:228.
  - Corrupt status.json: status command skips the metrics block
    silently (json.Unmarshal error → treat as missing). Exit 0.
  - Recursive mirror (`remote` ⊂ `local_path`): rclone catches at
    runtime ("can't sync or move files on overlapping remotes"),
    no destructive cycle. Smirror config doesn't pre-validate but
    rclone is the safety net.
  - Empty mirror name field: rejected at config validation.
  - Watcher-rename of project ROOT: handleRename emits TaskDelete
    with relPath=`.`; isUnsafeRelPath at sync.go:151-152 rejects
    `.` as unsafe. No destructive remote purge.
  - YAML anchors / `<<: *defs` merge: yaml.v3 parses correctly;
    smirror's strict-mode warns about unknown top-level fields
    (like `defaults:`) but parses the merged content fine.

  ARCHITECT
  ─────────
  - Daemon Ctrl+C: clean via context cancellation through engine
    workers. exec.CommandContext kills rclone subprocess. WAL
    auto-recovery on next open if mid-flight transactions.
  - withConfigLock coverage: ALL config mutators (AddMirror,
    RemoveMirror, SetField, including cmdRemoteSet's call to
    SetField) wrap the read-modify-write in withConfigLock.
  - Filter regeneration race: filter generation captured BEFORE
    rclone filter file is written (sync.go:771); skips the sync
    if filter changes during setup.

  SECURITY
  ────────
  - Hook injection via env vars: `containsShellMetachar` rejects
    metachars in SMIRROR_PROJECT/FILE/REMOTE/EVENT before exec.
    The hookCmd itself IS user-supplied config (intentional —
    "run my command").
  - ZIP path-traversal in selfupdate: `filepath.Base(f.Name)`
    matching means archive entries can't traverse; destPath comes
    from a freshly-created stage dir.
  - Selfupdate checksum / size caps: SM-200 + SM-201 closures.

  RELIABILITY
  ───────────
  - Disk full during sync: rclone surfaces error, smirror records
    KindSyncFailure anomaly, retries on transient codes (1, 5).
    State DB writes via WAL also surface errors.
  - Schema migration: partial-failure recovery is non-transactional
    — see §B.
  - last_vacuum_at write: see §C.

------------------------------------------------------------------------
E. STATE & NEXT
------------------------------------------------------------------------

  tip:                 578f4bf (0.9.77-dev)
  open BT bugs:        0 from impl side
  this memo's adds:    1 major (NEW-VAL-WIN-ACL), 2 low
                       (MIG-ATOMICITY, VACUUM-EBUSY)

  Most autonomous-recheck directions exhausted. Remaining areas
  worth probing if more time available:
    - Hard-link handling (two paths, one inode — what does
      sync_state DB do with this? Probably one entry per relPath,
      content-addressed dedup at rclone layer)
    - Concurrent CLI mutations during initial-sync (addmirror
      --initial-sync racing against unmirror)
    - The MSI uninstall path leaving runtime-created
      %ProgramData% files behind (related to NEW-VAL-WIN-ACL —
      privacy-by-deletion)
    - Long-path > 260 chars on Windows without `\\?\` prefix

  These are lower-prior than the v1.0 tag preparation work the
  impl side flagged in batch5 (release-dryrun, MSI smoke,
  CHANGELOG cleanup). NEW-VAL-WIN-ACL is the only one with a clear
  privacy-contract argument for blocking the v1.0 tag — and that's
  judgment call, not a hard requirement.

— validation, 2026-04-30 (7th memo, autonomous-recheck #4)
========================================================================
