# Inter-component contracts

**Audience**: SelectiveMirror maintainers + future-state architects.
**Companion to**:
- [`docs/PROPOSAL-2026-05-08-boundary-test-harvest.md`](./PROPOSAL-2026-05-08-boundary-test-harvest.md)
  (the test-class proposal that this doc enumerates the handoffs for)
- [`system-validation/installer_handoff_seam_test.go`](../system-validation/installer_handoff_seam_test.go)
  (the SM-216 ratchet — the template the harvest extends)
- [`docs/operations/release-runbook.md`](./operations/release-runbook.md)
  (pre-tag checklist references this doc)

## Why this doc exists

SelectiveMirror is a system of components that talk to each other.
Most of them are tested as units. The **seams between** components —
where component A produces state X for component B — were not
systematically tested before v1.0.0. **SM-216** (silent telemetry
failure on MSI consent) shipped because the MSI's registry-write
contract didn't match the daemon's state-DB-read expectation;
neither was wrong individually, but the contract between them was
implicit and untested.

This doc fixes the implicit-ness. **Each handoff is a contract**:
producer A guarantees X with these properties; consumer B reads X
under these assumptions. When the contract is written down,
boundary tests can enumerate failure modes (X is empty / null /
missing / corrupt / unexpected / stale) and the maintainer can ask
"is each of those tested?" before tagging a release.

The doc is written for the maintainer reviewing pre-tag readiness,
the new-maintainer onboarding into SelectiveMirror's architecture,
and the auditor asking "where's the contract surface?"

## How to read this doc

Each handoff has:

  - **Producer / Consumer** — which component emits, which receives.
  - **Crossing** — the wire/disk/syscall medium.
  - **Contract terms** — what the producer guarantees, what the
    consumer assumes.
  - **Invariants** — properties that must hold across the crossing
    (privacy, idempotency, ordering).
  - **Known boundary cases** — what happens when the contract slips.
    Where a real bug exists / existed, the SM-NNN ref is named.
  - **Test ratchets** — system-validation gate(s) that pin the
    contract. Where a ratchet doesn't yet exist, it's named as a
    candidate.

The 15 handoffs below are roughly in order from "load-bearing for
v1.0.x privacy-architecture" to "operational background." Read
top-down at first; jump by component name on later passes.

---

## 1. MSI installer → smirror runtime

| Field | Value |
|---|---|
| **Producer** | MSI installer (`installer/build-msi.ps1`, `installer/Package.wxs`, `installer/TelemetryConsent.wxi`) |
| **Consumer** | `smirror.exe` runtime (`cmd/smirror/main.go::cmdStart`, `internal/telemetry/install_events.go::SendInstallEventsIfDue`) |
| **Crossing** | Windows registry: `HKLM\Software\SelectiveMirror\TelemetryTier` (REG_SZ); plus filesystem (binary install path, no per-user state) |

### Contract terms

The MSI **MAY** write `TelemetryTier` to the registry with value in
`{"none", "standard", "reliability"}`. It **MUST NOT** write
`install_id`, `first_seen_at`, `last_recorded_version`, or any
other state-DB meta key — those are runtime concerns and the MSI
runs with elevated privileges (so its writes survive uninstall and
are admin-visible, both bad properties for an anonymous per-install
identifier).

If the user runs an unattended install with no `INSTALL_TELEMETRY_TIER`
property override, the MSI **MUST** write `TelemetryTier="none"` (the
privacy-honest default).

### Consumer assumptions

The runtime reads tier in a 3-step fallback (`internal/telemetry/tier.go::ReadTier`):
1. State DB meta `telemetry_tier` (preferred — runtime CLI writes here)
2. Registry `HKLM\Software\SelectiveMirror\TelemetryTier` (MSI writes here on install)
3. Default `none`

`install_id` is generated and persisted at runtime — either by
`cmdTelemetrySet` on tier transition out of None, OR by
`SendInstallEventsIfDue`'s idempotent recovery branch when tier is
non-None and install_id is missing (DEFECT-1 / SM-216 fix).

### Invariants

- **Privacy default**: silent install with no property override → tier
  ends up `none`. No telemetry without explicit operator action.
- **Anonymity boundary**: `install_id` is owned by runtime, not MSI.
  It is reset by `smirror clean` / state-DB delete; the registry-side
  identifier (TelemetryTier) is per-machine and survives uninstall.

### Known boundary cases

| Case | Resolution | Bug |
|---|---|---|
| MSI writes tier but not install_id; daemon reads view.InstallID="" | Runtime generates + persists install_id (idempotent recovery) | **SM-216** (DEFECT-1, fixed 8e82d40) |
| Three-radio dialog produces UX-distorted consent | Dialog reduced to two radios in v1.0.1 | **SM-217** (fixed 979697b) |
| Registry has TelemetryTier="garbage" (manual edit) | ReadTier `IsValid()` check → fail-closed to None | Tested: `TestReadTier_InvalidStateDBValueFailsClosed` (covers state DB). **Registry-side equivalent is candidate boundary test #1** |
| Registry has TelemetryTier="" (empty string) | Falls through to default None | **Candidate boundary test #2** — pin this behavior |
| Registry has TelemetryTier as REG_DWORD (wrong type) | Untested. RegistrySearch's Type="raw" might surface as a non-string; ReadTier → IsValid fail-closed | **Candidate** |
| Upgrade install with prior tier in registry | Skip dialog; preserve tier | Tested: `TestInstallerConsentDialog_PreservesExistingTierOnUpgrade` |
| MSI uninstall leaves TelemetryTier in registry; user reinstalls | Untested behaviorally | **Candidate boundary test #8** |
| HKLM (machine) and HKCU (user) tier values differ | Runtime reads HKLM only. Power-user CLI overrides via state DB. | **Candidate** |

### Test ratchets

- `system-validation/installer_consent_dialog_test.go` — dialog shape (4 tests, locked)
- `system-validation/installer_handoff_seam_test.go` — install_id boundary (4 tests, locked)
- `internal/telemetry/tier_test.go` — `IsValid` + `ReadTier` flow (5+ tests)

---

## 2. smirror runtime → state DB

| Field | Value |
|---|---|
| **Producer** | Runtime Go code (anywhere that calls `state.Store::SetMeta` / `SetSyncedFile` / `MarkDaemonStartup` etc.) |
| **Consumer** | Same runtime, later — across daemon restarts, CLI invocations, schema migrations |
| **Crossing** | SQLite database file at `<configdir>/state.db`; WAL mode |

### Contract terms

State DB has a documented schema (file-level: `internal/state/state.go::tableSchema`; meta-key-level: see comments in `state.go` + `install_events.go`). All schema changes go through the migration path (`internal/state/state.go::Open` runs idempotent CREATE-IF-NOT-EXISTS + migration steps).

Meta keys are dynamic key-value (TEXT, TEXT). Producers and consumers agree on key names by convention; **named constants in source are the single source of truth** (see `install_events.go::MetaFirstSeenAt` etc.).

### Consumer assumptions

A reader can assume:
- `state.db` is openable when the daemon's single-instance lock is held (concurrent readers from CLI commands fail-fast on lock conflict — `lock.ErrAlreadyRunning`).
- Meta keys read via `GetMeta(key)` return `("", nil)` when the key is unset.
- Meta keys read via `GetMeta(key)` return `(value, error)` when the DB cannot be queried — caller MUST treat error as "unknown state, fail closed."
- WAL mode means readers don't block writers and vice versa within a single process; cross-process correctness depends on the lock.

### Invariants

- **Schema forward-compatibility**: a binary at version N must accept a state DB written by binary N-1, applying migration steps. Documented as a pre-tag promise in `docs/iso-compliance.md`.
- **No backward migration**: a binary at version N MAY refuse to run against a state DB written by binary N+1 (downgrade refusal). The refusal must be a clear error, not silent corruption.
- **Lock holds across writes**: any write path acquires the single-instance lock; readers without the lock are best-effort.

### Known boundary cases

| Case | Resolution | Status |
|---|---|---|
| Binary version newer than schema (N runs against N-1's DB) | Migration path applies | **Candidate boundary test #3** — exercise across a real version step |
| Binary version older than schema (N runs against N+1's DB) | Refuse-with-clear-error | **Candidate boundary test #4** — pin the refuse-with-clear-error contract |
| State DB locked by another process | `state.Open` returns error; caller fails fast | Tested in `internal/lock/lock_test.go` |
| State DB corrupt (truncated, zero-byte) | `WasZeroByteOpen` flag; daemon logs warning, treats as fresh | Partially tested |
| Disk full mid-write | SQLite returns SQLITE_FULL; caller gets error | Untested at integration level |
| Meta key has unexpected value (`telemetry_tier='heLLO'` etc.) | `IsValid()` check at consumer; fail-closed | Tested for tier; **other meta keys lack analogous validation** |
| Meta key returns NULL vs empty string | SQLite TEXT type treats both as ""; consumer can't distinguish | Documented in `state.go::GetMeta` |
| Concurrent writes from two daemon instances | Single-instance lock prevents this; if lock somehow fails, SQLite-level WAL serializes | Lock failure path is in `lock_test.go` |

### Test ratchets

- `internal/state/state_test.go` — schema, migration, meta-key round-trip
- `internal/lock/lock_test.go` — concurrency boundary
- `system-validation/state_schema_migration_seam_test.go` — **candidate** (does not yet exist; would lock the migration contract)

---

## 3. smirror runtime → Cloudflare Worker

| Field | Value |
|---|---|
| **Producer** | Runtime client code (`internal/telemetry/contribute.go::Contribute`) |
| **Consumer** | Cloudflare Worker (`worker/src/index.ts`) at `https://smirror-telemetry.selectivemirror.workers.dev/v1/contribute` |
| **Crossing** | HTTPS POST + per-version HMAC-SHA256 signature |

### Contract terms

The runtime POSTs `{"payload": {...}, "claimed_version": "1.0.x", "claimed_hmac_hex": "<64-hex>"}`.
The HMAC is computed over the canonical-JSON serialization of `payload` MINUS `event_kind` (excluded so the dispatch field can change without breaking signature parity).

Endpoint is `POST /v1/contribute` only. All other paths return `410 Gone` (retired) or `404 not_found`.

### Consumer assumptions

The Worker assumes:
- Body is well-formed JSON with exactly the three top-level keys
- `payload` is a JSON object
- `claimed_version` is a non-empty string
- `claimed_hmac_hex` is a non-empty string

If the body shape is malformed, the Worker returns `400 bad_request`
WITHOUT forwarding to PostgREST (the FINDING-1 fix that closed the
PGRST schema-cache leak).

### Invariants

- **HMAC-first contract**: every POST that reaches PostgREST has been validated for shape and HMAC by the time it's dispatched server-side. Bad-shape requests die at the Worker.
- **No payload logging**: Worker does not log the request body (privacy-load-bearing).
- **Generic 502 on upstream failure**: if PostgREST or Supabase returns non-200, Worker rewrites to a generic `502 upstream_unavailable` JSON response (FINDING-4). Cloudflare-edge HTML 5xx CAN reach the client when the platform terminates the request before the Worker code runs (documented in `worker/README.md`).

### Known boundary cases

| Case | Resolution | Status |
|---|---|---|
| Malformed body shape | 400 from Worker, no PGRST passthrough | Tested: live-Worker probe + structural tests |
| HMAC mismatch | 200 with `{"ok":false,"error":"rejected"}` | Tested: smoke-test cases 1-2 |
| Bad event_kind | 200 with `{"ok":false,"error":"unknown_event"}` | Tested: smoke-test case 4 |
| Schema violation in payload | 200 with `{"ok":false,"error":"schema_violation:..."}` | Tested: smoke-test case 3 |
| Worker 410 Gone (legacy path) | Client treats as `ErrNetwork` | Tested in `contribute_test.go` |
| Upstream returns 5xx + text/html (Cloudflare-fronted Supabase blip) | Worker rewrites to JSON 502 | Tested via mock; **behavioral test under simulated upstream HTML 5xx is candidate boundary test #6** |
| Cloudflare-edge serves HTML 5xx without invoking Worker | Client treats as `ErrNetwork`; not Worker-fixable | Documented in `worker/README.md` |
| Probe URL mistyped (lands on different CF customer) | Worker probe fails on `cf-ray + SM-fingerprint` check | Tested: `check_response_came_from_cloudflare_sm_worker` |

### Test ratchets

- `system-validation/telemetry-worker-probe.py` — 11 live checks, daily CI cron
- `internal/telemetry/contribute_test.go` — 12 cases via httptest
- Worker source-property tests in `system-validation/telemetry_v2_worker_claims_test.go`

---

## 4. Cloudflare Worker → Supabase PostgREST

| Field | Value |
|---|---|
| **Producer** | Worker (after body validation) |
| **Consumer** | PostgREST at `https://qkspigvkniiiwxggdvbr.supabase.co/rest/v1/rpc/contribute` |
| **Crossing** | HTTPS POST with `apikey` + `Authorization: Bearer <SUPABASE_ANON_KEY>` + `Content-Profile: telemetry` |

### Contract terms

Worker forwards the validated body verbatim. PostgREST routes `/rpc/contribute` to the `telemetry.contribute(jsonb, text, text)` function via SECURITY DEFINER.

### Consumer assumptions

PostgREST's RPC binding maps body keys to function parameter names. The function MUST exist with the documented signature; PostgREST returns `404 PGRST202` if not (which the Worker now intercepts and rewrites; pre-fix this was a schema-cache leak — FINDING-1).

### Invariants

- **Anon-key only**: Worker forwards with the anon key, not service_role. The function is `SECURITY DEFINER` (runs as postgres) so it can write to rollup tables; the anon key just provides authentication.
- **Schema-cache reload after DDL**: any schema change requires `NOTIFY pgrst, 'reload schema'` to propagate; without it, PostgREST caches the old function signature and rejects new shapes.

### Known boundary cases

| Case | Resolution | Status |
|---|---|---|
| Function signature changed (renamed param, added/removed param) | PostgREST 404 PGRST202; Worker rewrites to 502 | Documented; rewrite tested |
| Anon key revoked | PostgREST 401; Worker rewrites to 502 | Documented; not behaviorally tested |
| PostgREST schema-cache stale (after DDL but before NOTIFY) | Same as signature changed | Documented in deploy-telemetry-v2.md |
| Wrong Content-Profile | PostgREST routes to `public` schema → 404 | Tested by deploy-time check |

### Test ratchets

- `scripts/telemetry-v2-smoke-test.py` (against live Supabase, deploy-time)
- `docs/operations/deploy-telemetry-v2.md` — schema-cache reload step is mandatory

---

## 5. Supabase PostgREST → Postgres function

| Field | Value |
|---|---|
| **Producer** | PostgREST (parameter-bound RPC call) |
| **Consumer** | `telemetry.contribute(payload jsonb, claimed_version text, claimed_hmac_hex text) RETURNS jsonb` (see `docs/telemetry-v2.sql`) |
| **Crossing** | PostgreSQL RPC binding (parameters not SQL-injectable; `pg_stat_statements` normalizes literals) |

### Contract terms

The function:
1. Verifies `not_object` → returns `schema_violation:not_object` (defensive ordering)
2. Computes canonical bytes from `payload - 'event_kind' - 'version_hmac'`
3. Calls `telemetry.verify_versioned_hmac(canonical, claimed_version, claimed_hmac_hex)`
4. Dispatches by `event_kind` to `_bump_install` / `_bump_bug` / `_bump_reliability`
5. Returns `{"ok": true}` or `{"ok": false, "error": "..."}`

Returns 200 on every legitimate outcome (rejected included). PostgreSQL exceptions thrown by the bump functions are caught and turned into `schema_violation` returns.

### Invariants

- **No raw payload storage**: only rollup-table UPSERTs are reachable from this function. Schema enforced via `system-validation/telemetry_schema_claims_test.go`.
- **Per-version HMAC**: the master key in Vault is mixed with `claimed_version` to derive the per-version key. Compromise of one binary's key invalidates only that version's signatures.
- **HMAC-first guard**: documented in `docs/telemetry-architecture-v2.md` "Threat model" with the explicit ordering note that `not_object` runs *before* HMAC.

### Known boundary cases

| Case | Resolution | Status |
|---|---|---|
| Vault secret `telemetry_master_key` missing | Function raises; PostgREST 500; Worker rewrites to 502 | Documented; not behaviorally tested |
| Vault secret rotated mid-call | Derived key mismatch; client gets `rejected` | **Candidate boundary test #7** |
| Function body raises unhandled exception | Caught by `EXCEPTION WHEN OTHERS` → schema_violation | Tested in smoke |
| Bucket value not in ENUM domain | UPSERT fails; caught → schema_violation | Tested |

### Test ratchets

- `docs/telemetry-v2.sql` — schema source
- `scripts/telemetry-v2-smoke-test.py` cases 1-5
- `system-validation/telemetry_schema_claims_test.go` — schema invariants

---

## 6. Postgres function → Vault

| Field | Value |
|---|---|
| **Producer** | Operator (sets the master key in Supabase Vault Studio) |
| **Consumer** | `telemetry.verify_versioned_hmac()` reads `vault.decrypted_secrets WHERE name = 'telemetry_master_key'` |
| **Crossing** | Vault decryption (Supabase-managed; secret never appears in logs / pg_stat_statements) |

### Contract terms

A row exists in `vault.decrypted_secrets` with `name='telemetry_master_key'` and a non-NULL `decrypted_secret`. The secret is the same one mixed by CI to derive each binary's per-version key (`SMIRROR_TELEMETRY_MASTER_KEY`).

### Invariants

- **Match between CI and Vault**: any binary built with one master key but deployed against a Vault with a different master key produces signatures that fail `verify_versioned_hmac`. Catastrophic-but-loud failure mode.
- **Operator-only rotation**: rotation requires updating BOTH Vault AND the CI secret simultaneously, plus a soak window for old binaries.

### Known boundary cases

| Case | Resolution | Status |
|---|---|---|
| Vault row missing | `verify_versioned_hmac` raises `'telemetry_master_key not configured in vault'` | Documented |
| Vault row has NULL decrypted_secret | Same as missing | Documented |
| Vault secret rotated; old binaries still in field | Old binaries' signatures fail; clients see `rejected`; new binaries succeed | **Candidate boundary test #7** — at minimum a docstring describing the rotation soak |

### Test ratchets

- Operator-only verification step in `docs/operations/deploy-telemetry-v2.md`
- No automated test today (Vault access requires service-role)

---

## 7. smirror runtime → rclone

| Field | Value |
|---|---|
| **Producer** | Runtime via `os/exec.Command` (`internal/rclone/detect.go`, `internal/sync/sync.go`) |
| **Consumer** | `rclone.exe` subprocess; output parsed from stdout |
| **Crossing** | Subprocess invocation, stdout/stderr capture, exit code |

### Contract terms

rclone v1.73+ supports the commands and flags smirror uses (`copyto --checksum`, `deletefile`, `moveto`, `lsjson --recursive --hash`, `copy --filter-from`). Major-version 2 is **rejected** (per SM-216 / GH#162) until validated.

### Consumer assumptions

- `rclone version` returns parseable output on stdout
- Sync commands return 0 on success, non-zero on failure
- stderr may contain banner / config-not-found warnings; smirror ignores those when stdout is well-formed

### Invariants

- **Version pinning**: `internal/rclone/detect.go::CompatCheck` returns `CompatNone` for major >= 2 with an explicit message
- **Per-backend pacer**: smirror runs ONE rclone process per backend (rclone's pacer handles per-backend rate limits internally)

### Known boundary cases

| Case | Resolution | Status |
|---|---|---|
| rclone binary not in PATH | `rclone.Detect` returns error; CLI exits 3 | Tested |
| rclone version too old (< 1.73) | `CompatCheck` returns CompatNone | Tested |
| rclone version too new (>= 2.x) | `CompatCheck` returns CompatNone | Tested (post-SM-216 / GH#162 fix) |
| rclone version string with build hash (`v1.73.5+abc`) | `parseVersion` strips suffix | Tested |
| rclone returns text/html on stderr (corporate proxy) | Ignored if stdout is parseable | **Candidate boundary test #5** |
| rclone subprocess hangs (stuck retry loop) | Untested kill-timeout | **Candidate boundary test #14** |

### Test ratchets

- `internal/rclone/detect_test.go`
- `test/run_tests.ps1` integration tests (use real local rclone)

---

## 8. smirror runtime → fsnotify / ReadDirectoryChangesW

| Field | Value |
|---|---|
| **Producer** | Windows kernel via ReadDirectoryChangesW (wrapped by `github.com/fsnotify/fsnotify`) |
| **Consumer** | `internal/watcher/watcher.go::FairQueue` |
| **Crossing** | Windows kernel API + Go channel |

### Contract terms

fsnotify delivers create/modify/rename/delete events for files under each watched mirror's local path. Events are best-effort; the kernel buffer can overflow under high write rate and events are silently dropped.

### Consumer assumptions

- The watcher MUST tolerate dropped events (full reconciliation runs cover this)
- Path strings come back with consistent separators (always backslash on Windows)
- Symlinks are reported (foreground mode follows; service mode rejects per SEC-H5 / PF-A3)

### Invariants

- **Eventual consistency**: missed event → next reconcile picks up the diff
- **Quiescence before sync**: 200ms stability + shared-read test before the file is touched

### Known boundary cases

| Case | Resolution | Status |
|---|---|---|
| Buffer overflow under load | Watcher falls back to full scan | Documented; not tested |
| Symlink in mirror tree (service mode) | Rejected per SEC-H5 | Tested in `system-validation/` |
| Symlink in mirror tree (foreground mode) | Followed by default | Documented as known asymmetry (R4-PF-10) |
| Path > MAX_PATH (260 chars, non-extended) | Untested | **Candidate** |
| Filesystem becomes read-only mid-sync | rclone error; sync engine retries | Documented |

### Test ratchets

- `internal/watcher/watcher_test.go`
- SLA smoke (`test/sla_smoke.ps1`) covers throughput limit but not buffer overflow specifically

---

## 9. smirror runtime → Windows SCM

| Field | Value |
|---|---|
| **Producer** | `smirror service install/start/stop/uninstall` (`internal/service/service.go`) |
| **Consumer** | Windows Service Control Manager (admin-only) |
| **Crossing** | Windows API via `golang.org/x/sys/windows/svc/mgr` |

### Contract terms

Service runs as LocalSystem; admin-owned config required (SEC-C5). The service binary is the same `smirror.exe` that ran the install command.

### Invariants

- **Admin gate**: install / uninstall require admin token (SEC-C5 + SEC-H1/H2)
- **Single-instance lock applies**: service mode and foreground mode cannot both run on the same data dir

### Known boundary cases

| Case | Resolution | Status |
|---|---|---|
| Service install without admin | Refuses with clear error | Tested |
| Service uninstall while running | Auto-stops first | Documented |
| Service binary swapped on disk while running | SCM keeps the old in-memory version until restart | Known limitation; documented |
| Service runs as non-LocalSystem (custom config) | Refused at install time | Tested |

### Test ratchets

- `internal/service/service_test.go`
- `installer/smoke-test.ps1` (used by release-dryrun)

---

## 10. smirror runtime → Windows Scheduled Task

| Field | Value |
|---|---|
| **Producer** | `smirror task install/start/stop/uninstall` (`internal/task/task.go`) |
| **Consumer** | Windows Task Scheduler (per-user, no admin) |
| **Crossing** | `schtasks.exe` + XML task definition |

### Contract terms

Task is per-user; runs at user logon or daemon-start. No admin token required.

### Invariants

- **Per-user scope**: task is registered to the current user's Scheduled Tasks tree, not the machine-wide tree
- **No service-mode collision**: `smirror task install` refuses if the SCM service is also installed (single-active-mode invariant)

### Known boundary cases

| Case | Resolution | Status |
|---|---|---|
| Task already exists (re-install) | Uninstall + reinstall | Tested |
| User logged out — does task run? | Default config requires logon; "run when not logged on" is opt-in | **Candidate boundary test #11** |
| schtasks XML format unparseable | schtasks returns non-zero; install fails | Documented; not tested |

### Test ratchets

- `internal/task/task_test.go`

---

## 11. smirror runtime → notification system

| Field | Value |
|---|---|
| **Producer** | `internal/notify/notify.go` |
| **Consumer** | Windows toast notification system |
| **Crossing** | Win10+ Notification API (when available); fallback to logging |

### Contract terms

Best-effort. Failure to deliver a notification does not affect smirror's correctness path.

### Invariants

- **Rate-limited**: notifications are deduped + rate-limited per sender to prevent toast-spam under burst conditions
- **Optional**: `is_notify_enabled` config gate disables entirely

### Known boundary cases

| Case | Resolution | Status |
|---|---|---|
| Notification API unavailable (older Windows) | Fall back to log-only | Tested |
| User has notifications disabled in OS | Toast simply not rendered; no error | Documented |

### Test ratchets

- `internal/notify/notify_test.go`

---

## 12. CLI commands → smirror runtime

| Field | Value |
|---|---|
| **Producer** | `smirror <subcommand>` invocations from any shell |
| **Consumer** | `cmd/smirror/main.go` dispatch + each cmd*.go handler |
| **Crossing** | Process invocation with command-line arguments, stdin, stdout, stderr |

### Contract terms

Each subcommand reads its config + state DB independently. Lock conflicts (when daemon is running) produce `ExitLockConflict` (4) for tier-mutating CLI commands.

### Invariants

- **Read-only commands work without lock**: `status`, `dry-run`, `explain`, `list-filters`, `project-stats` all read-only
- **Tier-mutating commands acquire lock briefly**: `addmirror`, `unmirror`, `telemetry [tier]` acquire the config-edit lock
- **CLI does NOT fire install events**: per FINDING-19 ("CLI is a deterministic primitive"), only the daemon's startup hook fires `SendInstallEventsIfDue`

### Known boundary cases

| Case | Resolution | Status |
|---|---|---|
| `smirror addmirror` while daemon running | Lock conflict; exit 4 | Tested |
| `smirror sync-now` while daemon running | Acquires lock; if already held, sends signal to running daemon | Tested |
| `smirror status` on virgin state DB | Lenient loader returns sensible defaults | Tested (post-FINDING 5) |
| `smirror telemetry standard` with no config | Lenient path; tier flips, install_id generated | Tested |

### Test ratchets

- `cmd/smirror/cmd_*_test.go` per command
- `system-validation/cli_test.go`

---

## 13. selfupdate → GitHub Releases API

| Field | Value |
|---|---|
| **Producer** | GitHub Releases API |
| **Consumer** | `cmd/smirror/selfupdate.go` via `internal/telemetry/telemetry.go::CheckForUpdate` |
| **Crossing** | HTTPS GET `api.github.com/repos/qraveh/SelectiveMirror/releases/latest` |

### Contract terms

Returns a JSON object with `tag_name`, `name`, `body`, `assets[]`. Asset names follow the release-tagging convention: `SelectiveMirror.msi`, `SelectiveMirror_<version>_windows_amd64.zip`, `checksums.txt`.

### Consumer assumptions

- Tier gate applies: at None tier, the API call MUST NOT fire (covered by `selfupdate.go`'s tier check)
- Authentication: uses `gh auth token` with 2s timeout (FINDING SM-174 fix), falls back to `GITHUB_TOKEN` env, falls back to unauthenticated

### Invariants

- **No telemetry leakage from selfupdate**: the API call doesn't include any install identity beyond the User-Agent
- **Privacy at None**: the API call doesn't fire at None tier per the privacy contract

### Known boundary cases

| Case | Resolution | Status |
|---|---|---|
| API rate limit hit | Caller logs error; no exit | Tested |
| API returns 200 but malformed JSON | json.Unmarshal error; caller surfaces | Tested |
| API returns asset list missing the MSI | Caller fails with clear error | **Candidate boundary test #12** |
| `gh auth token` hangs | 2s timeout (SM-174) | Tested |

### Test ratchets

- `internal/telemetry/telemetry_test.go::TestCheckForUpdate*`

---

## 14. report-bug → GitHub Issues web form

| Field | Value |
|---|---|
| **Producer** | `cmd/smirror/cmd_report_bug.go` |
| **Consumer** | GitHub Issues at `https://github.com/qraveh/SelectiveMirror/issues/new?template=bug_report.yml&...` |
| **Crossing** | Browser URL with prefilled `&title=...&environment=...&logs=...` query params |

### Contract terms

The user reviews the bundle in their browser; THEY decide whether to submit. The narrative content is hosted by GitHub, not SelectiveMirror.

### Invariants

- **Sanitized before prefill**: paths, mirror names, credentials, remote URIs are redacted before the URL is built
- **URL length cap**: 8KB; truncated with smart per-field cuts
- **Always-print URL on `--submit`**: per the 2026-05-02 rule, every `--submit` prints the GitHub URL whether the telemetry POST succeeded or not (SelectiveMirror "does not accept ownership of the data of bug reports")

### Known boundary cases

| Case | Resolution | Status |
|---|---|---|
| URL exceeds 8KB | Per-field truncation, then drop logs | Tested |
| User Cancels in browser | No data sent (browser-side decision) | N/A |
| User pastes the URL into a chat (could leak the bundle) | Out of scope; user choice | Documented |

### Test ratchets

- `cmd/smirror/cmd_report_bug_test.go`
- `cmd/smirror/cmd_report_bug_submit_test.go` (privacy invariants for the categorical bucket)

---

## 15. State DB schema migrations

| Field | Value |
|---|---|
| **Producer** | Binary version N+1 (running for the first time against a state DB written by N) |
| **Consumer** | Same binary, after migration completes |
| **Crossing** | `internal/state/state.go::Open` invokes idempotent CREATE-IF-NOT-EXISTS + migration steps |

### Contract terms

Schema is forward-compatible across patch versions. Migration steps are idempotent (re-running doesn't corrupt). Each migration step is documented in `state.go` source comments.

### Invariants

- **Forward-only by default**: a v1.0.x binary opening a v1.0.0 DB applies migrations; a v1.0.0 binary opening a v1.0.x DB **MUST NOT silently load** — it must refuse with a clear error
- **Atomicity per migration step**: a migration that creates a column AND populates a default value is wrapped in a transaction; a partial state should be impossible

### Known boundary cases

| Case | Resolution | Status |
|---|---|---|
| Binary newer than DB | Migration applies forward | **Candidate boundary test #3** |
| Binary older than DB | Refuse-with-clear-error | **Candidate boundary test #4** |
| Migration step fails halfway | Transaction rollback; binary refuses to start | Documented; **untested** |
| Schema version recording corrupted | Untested | **Candidate** |

### Test ratchets

- `internal/state/state_test.go` (basic schema operations)
- **Candidate**: `system-validation/state_schema_migration_seam_test.go` (cross-version test fixtures)

---

## How to use this doc

### When writing new code that crosses a seam

Read the contract for the relevant pair before designing the call site.
If the new code introduces a new contract term, **add it to this doc in
the same commit** so the implicit-ness doesn't return.

### When reviewing pre-tag readiness

Walk the 15 handoffs. For each, look at "Known boundary cases" and ask:
**"Does the candidate test exist? If not, am I shipping the bug?"**

That walk would have caught SM-216 — the question "what if MSI sets
tier without install_id?" was simply never asked before v1.0.0
shipped. With this doc on the desk, the question is enumerated and
the missing test is named explicitly.

### When investigating a post-release bug

Find the seam. The bug almost certainly lives at one of the 15
crossings; the contract terms tell you which side made the wrong
assumption. Then add the missing boundary test to ratchet it in.

---

## Maintenance

- **When a new component is added**: add a new section. The 15-row
  count is not a ceiling.
- **When a contract term changes**: update both the producer and
  consumer descriptions in the same commit. If a producer is
  loosening (accepting wider input) and the consumer is tightening
  (assuming narrower input), the version mismatch will eventually
  break some user — write a boundary test that catches the slip.
- **When SM-NNN-class bugs are filed against a seam**: add them to
  the "Known boundary cases" table for that seam. The doc becomes
  more useful as it accumulates real-world failure modes.

This doc lives at `docs/inter-component-contracts.md` and is the
current snapshot. Future-state revisions should be PR-reviewable
diffs, not whole-doc rewrites.
