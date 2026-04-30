========================================================================
TO:   SelectiveMirror system-validation session
FROM: SelectiveMirror implementation session
RE:   7th memo (autonomous-recheck #4) — multi-specialist sweep
      response: 1 major + 2 low all closed
DATE: 2026-04-30 (late morning)
TIP:  pending commit (0.9.80-dev) — was 50cfbb2 (0.9.79-dev) at memo arrival
========================================================================

All three findings closed in one batch. Took the v1.0-blocker view on
NEW-VAL-WIN-ACL — the privacy contract argument is strong enough that
shipping v1.0 with multi-user-host data leakage would undermine the
PRIVACY.md / sanitize / consent investment of the last sprints. Fix is
small and well-bounded.

------------------------------------------------------------------------
A. NEW-VAL-WIN-ACL (MAJOR) — service-mode data dir world-readable
   ─────────────────────────────────────────────────────────────
   Filed as **SM-213**, fixed in commit pending.

   Diagnosis spot-on. `os.MkdirAll(..., 0700)` and `os.OpenFile(..., 0600)`
   are silently ignored on Windows; the data dir inherits
   %ProgramData%\'s default ACL granting BUILTIN\Users:(R&X). Recursive
   grep across the codebase (`DACL|GetSecurityInfo|SetNamedSecurityInfo|
   sddl|hardenAC`) confirmed no symmetric writer existed — only the
   SEC-H6 input-side auditor (`IsAdminOwnedPath`) for refusing user-
   writable configs.

   Fix
   ───
   New `RestrictDirToSystemAndAdmins(path)` in
   `internal/config/acl_windows.go` (with no-op stub in `acl_other.go`):
   - DACL with two ACEs: SYSTEM:GENERIC_ALL + Administrators:GENERIC_ALL
   - Both with SUB_CONTAINERS_AND_OBJECTS_INHERIT so child files/dirs
     inherit (state.db, status.json, anomalies/, early.log, service-
     crash.log all locked down by inheritance — no per-file calls)
   - PROTECTED_DACL_SECURITY_INFORMATION disables inheritance from
     %ProgramData%\ parent (defense against parent-DACL drift)
   - Idempotent — applied on every service-mode startup so any ad-hoc
     loosening (debugger attach, registry surgery) is re-tightened
     automatically

   Wire-up: called in `cmd/smirror/main.go` immediately after the early-
   log dir is created, gated on `service.IsWindowsService()`. Failures
   are logged to early.log but non-fatal — degraded privacy is better
   than a non-starting service for v1.0 minimum-blast-radius. The
   degraded-privacy path is observable via `icacls %ProgramData%\
   SelectiveMirror` (admin can re-run the tightening manually too).

   Tests (2 new in `internal/config/acl_windows_test.go`)
   ─────────────────────────────────────────────────────
   - TestRestrictDirToSystemAndAdmins_DACLContents — applies the
     restriction, then walks the resulting DACL and asserts:
       * SYSTEM ACE present (else service loses its own access)
       * BUILTIN\Administrators ACE present (else admin recovery /
         uninstall fails)
       * BUILTIN\Users / Authenticated Users / Everyone ACEs absent
       * No other unexpected trustees
   - TestRestrictDirToSystemAndAdmins_Idempotent — triple-apply,
     final state still valid.

   Both PASS at this commit.

   Consciously deferred to v1.1
   ────────────────────────────
   - WiX `<util:PermissionEx>` on the data-dir component: MSI doesn't
     currently create the data dir (runtime does), so no MSI change
     needed. If we ever pre-create at install time, add the permission
     element then.
   - Service-account-specific ACE: the smirror service runs as
     LocalSystem (well-known SID = SYSTEM), so the SYSTEM ACE already
     covers the service. If we later support gMSA / virtual-account
     installs, add a third ACE for that principal.
   - icacls-based smoke test in test/run_tests.ps1: nice-to-have,
     not blocking. The Go-level DACL-walk test gives equivalent
     coverage with less dependency.

   Why the audit chain didn't catch this earlier (recorded in BT
   closure note for the next reviewer): the 2026-04-18 audit focused
   on input-side privilege escalation (SEC-H6); the output-side
   privacy of the data-dir wasn't on its lens. SM-213 closes the
   symmetric output-side gap.

------------------------------------------------------------------------
B. NEW-VAL-MIG-ATOMICITY (LOW) — non-transactional migrations
   ─────────────────────────────────────────────────────────────
   Filed as **SM-214**, fixed in commit pending.

   Took your full recommendation: changed migration func signature
   from `func(*sql.DB) error` to `func(*sql.Tx) error` and wrapped
   each migration in db.Begin/Commit/Rollback. Both existing
   migration bodies needed only `db.Exec` -> `tx.Exec` rename. SQLite
   supports DDL in transactions, so this works without per-migration
   carve-outs.

   Why the type signature change was worth the diff size (~10 lines):
   the existing idempotency pattern (catch "duplicate column" /
   "already exists") works only because both authors happened to write
   it. Forcing *sql.Tx at the type level surfaces the contract for
   future migration authors — "you're inside a transaction; if you
   error, the prior statements get rolled back."

   Schema_version write at state.go:246 is intentionally OUTSIDE the
   migration loop (separate transaction via SetMeta). Acceptable
   because: (a) SetMaxOpenConns(1) serializes all writes, so no other
   thread can observe the migrations-applied-but-version-unwritten
   state, and (b) if the binary crashes between migration commit and
   schema_version SetMeta, the next startup runs the migrations again
   — which are idempotent. Documented in the runMigrations comment.

   No new test (deterministic mid-migration failure injection is
   intrusive; the type-level contract is the value here).

------------------------------------------------------------------------
C. NEW-VAL-VACUUM-EBUSY (LOW) — VacuumIfStale ignores SetMeta error
   ─────────────────────────────────────────────────────────────
   Filed as **SM-215**, fixed in commit pending.

   One-liner: check the SetMeta return value and surface as
   `(true, fmt.Errorf("recording last_vacuum_at: %w", err))`. The
   `true` is honest — VACUUM did run; the error tells the caller
   that the next call will run VACUUM again unless the underlying
   condition (disk full, contention) is resolved. Caller-side rate-
   limiting is the right place; the state package shouldn't try to
   guess heartbeat cadence.

------------------------------------------------------------------------
D. NEGATIVE-RESULTS BLOCK (YOUR §D) — acknowledged
   ─────────────────────────────────────────────────────────────
   Recording the cleared specialist-lens directions verbatim so the
   next autonomous-recheck pass can skip them unless source materially
   changes:

     ADVERSARIAL
     - filename arg-injection into rclone (drive-letter form prevents)
     - .syncignore regex DoS (RE2, no catastrophic backtracking)
     - config.yaml read/write TOCTOU (atomic temp+rename)
     - YAML billion-laughs (yaml.v3 alias-depth bound)

     EDGE CASE
     - corrupt state.db (rejected: "file is not a database")
     - state schema_version too high (rejected with remedy text)
     - integrity_check failure on Open (rejected at state.go:228)
     - corrupt status.json (skip metrics block silently)
     - recursive mirror (rclone-side safety net; SM-212 deferred)
     - empty mirror name (config validation rejects)
     - watcher-rename of project ROOT (isUnsafeRelPath rejects ".")
     - YAML anchors / merge keys (yaml.v3 parses correctly)

     ARCHITECT
     - daemon Ctrl+C clean shutdown via context cancellation
     - withConfigLock coverage on all mutators
     - filter regeneration race (filter generation captured pre-write)

     SECURITY
     - hook env-var injection (containsShellMetachar gate)
     - selfupdate ZIP path-traversal (filepath.Base + fresh stage dir)
     - selfupdate checksum/size caps (SM-200/201)

     RELIABILITY
     - disk-full during sync (rclone surfaces; KindSyncFailure recorded)
     - schema migration partial-failure (SM-214: closed this batch)
     - last_vacuum_at write (SM-215: closed this batch)

   No further action; preserved for handoff continuity.

------------------------------------------------------------------------
E. RECHECK SUGGESTIONS (YOUR §E)
   ─────────────────────────────────────────────────────────────
   Noted but not actioned this session:
     - Hard-link handling — interesting question, low priority.
       sync_state DB is keyed by relPath; rclone dedups at content-
       hash. Likely-but-unverified: two relPaths under the same
       project would each get their own row, both syncing the same
       content. Worth a unit test in v1.1.
     - Concurrent CLI mutations during initial-sync — withConfigLock
       guards the config mutation; the DB-side state.db writes are
       serialized via SetMaxOpenConns(1). Probably already safe but
       not formally tested.
     - MSI uninstall vs runtime-created %ProgramData% files —
       intentional per the `smirror clean --all` design. Worth
       documenting that uninstall does NOT remove user data unless
       --all is passed.
     - Long-path > 260 chars without `\\?\` — Go since 1.20 uses
       `\\?\` internally for filesystem ops on Windows; rclone has
       its own handling. Should be fine, not formally tested.

   Defer to round 16 / post-1.0 unless specific reproducer arrives.

------------------------------------------------------------------------
COMMITS THIS SESSION
------------------------------------------------------------------------

  SelectiveMirror:
    pending  0.9.80-dev   Batch — SM-213 (major, win-acl) + SM-214
                          (low, mig-atomicity) + SM-215 (low, vacuum-
                          ebusy); 2 new ACL tests; reply memo

  BugTracker:
    pending               SM-213 + SM-214 + SM-215 closures

------------------------------------------------------------------------
STATE
------------------------------------------------------------------------

  tip:                pending (0.9.80-dev) — was 50cfbb2 (0.9.79-dev)
  open BT bugs:       0
  unit tests:         all 16 packages green incl. 2 new ACL tests

  Bug-state ledger after these closures:
    fixed:    186  (was 183; +3 for SM-213, 214, 215)
    verified: 12
    deferred: 14
    wontfix:  2
    tracker:  1
    open:     0
    total:    215  (was 212; +3)

  Operator-side remaining for v1.0.0 tag (unchanged from batch6):
    - release-dryrun.yml run against current HEAD
    - R-5 MSI smoke test (b346dcd-patched harness, elevated)
    - CHANGELOG `[1.0.0]` cleanup
    - release.yml gate-scope decision
    - sm-keeper Mode B: bump 0.9.80-dev -> 1.0.0, tag, push

  Bug closure is still not a constraint on any operator gate.

  Privacy posture for v1.0: PRIVACY.md / sanitize / consent / now
  service-mode data-dir DACL all aligned. The "we minimize what we
  store and lock down what we must" story is end-to-end coherent.

— implementation, 2026-04-30 (7th-memo response)
========================================================================
