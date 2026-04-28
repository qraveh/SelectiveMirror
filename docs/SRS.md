# Software Requirements Specification (SRS)

## SelectiveMirror — Selective Near-Real-Time File Mirror

**Document Version**: 1.1 (ISO compliance revision) — 2026-04-27
**Date**: 2026-04-06 (baseline 1.0); 2026-04-18 (status refresh); 2026-04-27 (1.1 ISO compliance revision)
**Author**: Raveh / Claude (iterative collaboration)
**Project Version**: 0.9.7-dev (source-current at this revision)
**Status**: BASELINED — approved for v1.0 release planning. ISO compliance status: **Partial** (see `docs/iso-compliance.md`).

---

## Table of Contents

1. [Introduction](#1-introduction)
2. [Overall Description](#2-overall-description)
3. [Functional Requirements](#3-functional-requirements) (incl. 3.11 Anomaly Detection)
4. [Non-Functional Requirements (ISO/IEC 25010)](#4-non-functional-requirements-isoiec-250102023)
5. [Interface Requirements](#5-interface-requirements)
6. [Data Requirements](#6-data-requirements)
7. [Constraints & Assumptions](#7-constraints--assumptions)
8. [Future Requirements](#8-future-requirements) (incl. 8.3 Committed v1.0, 8.4 Post-v1.0)
9. [Competitive Gap Analysis](#9-competitive-gap-analysis)
10. [Appendices](#10-appendices)
11. [Implementation Plan](#11-implementation-plan)

---

## 1. Introduction

### 1.1 Purpose

This SRS defines the functional and non-functional requirements for SelectiveMirror, a Windows-first file synchronization service that mirrors local directories to cloud/remote backends via rclone. 
It serves as the authoritative specification for what the system must do, how well it must do it, and what it must not do.

### 1.2 Scope

SelectiveMirror is a single-binary background service that:
- Detects local file changes in near-real-time
- Applies configurable include/exclude filters
- Mirrors qualifying changes to any rclone-supported backend (70+)
- Provides diagnostics, health monitoring, and drift detection

It is **not**: a bidirectional sync engine, a file sharing platform, a backup system with versioning/retention, or a replacement for rclone itself.

### 1.3 Definitions

| Term | Definition |
|------|-----------|
| **Mirror** | A configured mapping from a local directory to a remote rclone destination |
| **Ghost** | A remote file with no valid local counterpart (orphan or leak) |
| **Orphan** | Remote file whose local source no longer exists |
| **Leak** | Remote file whose local source is now excluded by filters |
| **Quiescence** | The state where a file's size and mtime have been stable long enough to be considered complete |
| **Reconciliation** | Periodic full-directory comparison to catch changes missed by filesystem events |
| **Circuit breaker** | Mechanism that backs off retries after consecutive failures |
| **FairQueue** | Scheduling queue providing deduplication, priority, per-file cooldown, and fairness |
| **Backend** | Any rclone-supported remote storage (Google Drive, S3, SFTP, etc.) |

### 1.4 Applicable Standards

| Standard | Scope of Application | How Applied |
|----------|---------------------|-------------|
| **ISO/IEC/IEEE 29148:2018** | Requirements engineering — processes and products | Document structure, requirement attributes (ID, priority, status, rationale), traceability matrix |
| **ISO/IEC 25010:2023** | Product quality model — quality characteristics | Non-functional requirements (Section 4) organized by 25010 characteristics |
| **ISO/IEC 25023:2016** | Quality measurement — quality measures | Metrics and targets in Section 4 derived from 25023 measurement functions |
| **ISO/IEC/IEEE 29119:2023** | Software testing — processes, documentation, techniques | Test plan, test design (per-feature tables), test techniques (EP/BVA/DT/ST) — see `docs/VV-Plan.md` |

Compliance status against each standard — including gaps and remediation actions — is tracked in `docs/iso-compliance.md` (single source of truth for ISO compliance audits).

### 1.5 References

- rclone documentation: https://rclone.org/docs/
- `.gitignore` pattern specification: https://git-scm.com/docs/gitignore
- Windows ReadDirectoryChangesW API documentation
- SQLite WAL mode documentation

---

## 2. Overall Description

### 2.1 Product Perspective

SelectiveMirror fills a gap between manual rclone usage and full sync clients (Google Drive Desktop, Dropbox, OneDrive). Existing tools either:
- Sync everything (no exclusion semantics, no `.gitignore`-style filtering)
- Require manual invocation (no real-time watching)
- Don't run as Windows services
- Can't target arbitrary rclone backends

SelectiveMirror is the **missing link**: real-time + selective + backend-agnostic + Windows service.

### 2.2 Product Functions (High-Level)

| Function | Description |
|----------|-------------|
| **Watch** | Detect filesystem changes via OS-native APIs |
| **Filter** | Apply `.gitignore`-syntax rules to include/exclude files |
| **Sync** | Upload changed files to remote via rclone |
| **Delete** | Handle local deletions per configurable policy |
| **Reconcile** | Periodically verify local-remote consistency |
| **Diagnose** | Detect drift, ghosts, configuration errors |
| **Service** | Run as a Windows background service |

### 2.3 User Classes

| User Class | Description | Primary Concerns |
|-----------|-------------|-----------------|
| **Developer** | Uses SelectiveMirror to back up code projects | Filter accuracy, `.git` exclusion, low latency |
| **Knowledge worker** | Mirrors documents, notes, research | Office file handling, large file support |
| **System administrator** | Installs/configures as a Windows service | Reliability, diagnostics, recovery |
| **AI orchestrator** | Uses mirrored files for cross-tool workflows | Near-real-time latency, multi-project isolation |

### 2.4 Operating Environment

- **OS**: Windows 10/11 (x64). Cross-platform build possible but untested.
- **Runtime**: Single static binary, no runtime dependencies beyond rclone
- **Storage**: Local NTFS/ReFS filesystem as source; any rclone backend as target
- **Resources**: ~15 MB RAM idle, ~50 MB under load; minimal CPU unless syncing

### 2.5 Design Philosophy

1. **Explicit over implicit** — No silent data loss; every destructive action requires configuration
2. **rclone as the transport layer** — SelectiveMirror never reimplements backend protocols
3. **Single-instance safety** — One process, one state database, no corruption
4. **Fail-open for reads, fail-closed for writes** — Status/diagnostics always work; sync failures don't escalate
5. **Developer-first UX** — `.gitignore` syntax, CLI-native, no GUI required

---

## 3. Functional Requirements

*All functional requirements follow ISO/IEC/IEEE 29148:2018 attributes: unique ID, requirement statement (SHALL/SHOULD/MAY), priority (MoSCoW), rationale, and implementation status. Rationale is provided where the "why" is not self-evident.*

### 3.1 File Watching (FR-WATCH)

| ID | Requirement | Priority | Rationale | Status |
|----|------------|----------|-----------|--------|
| FR-WATCH-01 | System SHALL detect file create, modify, delete, and rename events in watched directories using OS-native APIs (ReadDirectoryChangesW on Windows) | Must | OS-native APIs are the only way to achieve sub-second detection without polling | Done |
| FR-WATCH-02 | System SHALL recursively watch all subdirectories under each mirror's local path | Must | — | Done |
| FR-WATCH-03 | System SHALL detect new subdirectory creation and begin watching automatically | Must | Projects grow organically; manual re-registration is unacceptable | Done |
| FR-WATCH-04 | System SHALL handle subdirectory deletion without crashing the watcher | Must | Editors and build tools create/delete dirs rapidly | Done |
| FR-WATCH-05 | System SHALL reject watching symlink-to-directory targets (prevent directory escape) | Must | Symlink-to-dir could sync files outside project boundary (security) | Done |
| FR-WATCH-06 | System SHALL resolve and sync symlink-to-file targets during startup/forced/initial syncs. Real-time change detection applies only to targets within the mirror tree. Symlinks to external targets SHALL be logged at INFO level (not warning). | Should | Common in monorepos; content matters, not link structure. External targets are a normal usage pattern, not an error condition. | Done |
| FR-WATCH-07 | System SHOULD detect burst-delete scenarios (>N deletes in window) and trigger accelerated reconciliation | Should | OS event buffer overflow drops events during mass deletes (SM-050) | Done |
| FR-WATCH-08 | System SHALL NOT crash or lose state when filesystem events are dropped by the OS | Must | Event drops are normal under load; crash = data loss window | Done |
| FR-WATCH-09 | System SHALL support watching multiple independent mirror directories simultaneously | Must | Users have multiple projects | Done |
| FR-WATCH-10 | System SHALL use USN journal for fast restart reconciliation (gap detection between stop and start) and for detecting changes lost during brief watcher failures (e.g., ReadDirectoryChangesW buffer overflow during burst writes) | Should | Full reconciliation is O(files); USN journal is O(changes-since-shutdown). Also covers silent event drops during buffer overflow — events the OS never delivers. | Planned (Phase 3) |
| FR-WATCH-11 | System SHALL support configurable watch depth per mirror (default: unlimited). Depth 0 = top-level files only; depth N = N levels of subdirectories. | Could | Some users want shallow watching (e.g., sync only top-level config files, not deep build artifacts) | Not Done |

### 3.2 Filtering (FR-FILTER)

| ID | Requirement | Priority | Rationale | Status |
|----|------------|----------|-----------|--------|
| FR-FILTER-01 | System SHALL support `.gitignore`-syntax patterns for file exclusion, validated against a gitignore conformance test suite covering edge cases (`**/foo`, `foo/**/bar`, trailing spaces, character classes, escaped characters) | Must | Developers already know gitignore; zero learning curve. Conformance must be verified, not assumed. | Done (conformance suite: Not Done) |
| FR-FILTER-02 | System SHALL support per-mirror `.syncignore` files in the mirror root | Must | Different projects need different rules | Done |
| FR-FILTER-03 | System SHALL support global exclude patterns in configuration | Must | Common patterns (`.git/`, `node_modules/`) shouldn't repeat per project | Done |
| FR-FILTER-04 | System SHALL merge global + per-mirror filters with last-match-wins semantics | Must | Matches gitignore spec; enables negation overrides (SM-036/037) | Done |
| FR-FILTER-05 | System SHALL support negation patterns (`!important.log`) to re-include excluded files | Must | Required by gitignore spec; critical for whitelisting | Done |
| FR-FILTER-06 | System SHALL hot-reload filters when `.syncignore` files change (no restart required) | Must | Service may run for weeks; filter edits must take effect immediately | Done |
| FR-FILTER-07 | System SHALL track filter generations to detect stale sync tasks after hot-reload | Must | Race between task enqueue and filter change causes wrong-filter sync (SM-044) | Done |
| FR-FILTER-08 | System SHALL generate rclone-compatible filter files for batch operations | Must | Batch sync uses `rclone copy --filter-from`; filter must translate correctly | Done |
| FR-FILTER-09 | System SHALL provide a command to list effective filter rules per mirror | Must | Users need to audit merged filter state | Done |
| FR-FILTER-10 | System SHALL provide a command to explain whether a specific file is included/excluded and which rule matched | Must | Debugging "why isn't this file syncing?" is the #1 support question | Done |
| FR-FILTER-11 | System SHALL reject malformed `.syncignore` files, log the parse error, and continue with the previous valid filter generation | Must | Fail-open (sync everything) or fail-closed (sync nothing) on bad syntax are both dangerous. Preserving last-known-good is the only safe behavior. | Done (v0.5.0) |

### 3.3 Synchronization (FR-SYNC)

| ID | Requirement | Priority | Rationale | Status |
|----|------------|----------|-----------|--------|
| FR-SYNC-01 | System SHALL sync individual file changes via `rclone copyto --checksum` | Must | Per-file sync with checksum avoids redundant uploads | Done |
| FR-SYNC-02 | System SHALL perform batch sync on startup via `rclone copy` per mirror | Must | Batch is O(1) rclone calls vs O(N) per-file; startup was 170s → 15s after this fix | Done |
| FR-SYNC-03 | System SHALL verify quiescence before syncing (file stable for configurable duration) | Must | Office apps write in multiple stages; syncing mid-write uploads partial files | Done |
| FR-SYNC-04 | System SHALL skip syncing files that are exclusively locked by another process | Must | Locked files cannot be read; attempting sync would fail and waste queue cycles | Done |
| FR-SYNC-05 | System SHALL skip syncing files that exceed the configured max file size | Must | Protects bandwidth and API quota from accidental large-file sync | Done |
| FR-SYNC-06 | System SHALL use MD5 checksums to avoid unnecessary uploads of unchanged files | Must | Matches rclone's native checksum for Google Drive and most backends | Done |
| FR-SYNC-07 | System SHALL support configurable bandwidth limiting | Should | Users on metered connections need throttling | Done |
| FR-SYNC-08 | System SHALL support configurable concurrent sync workers (1-16) | Should | Parallelism vs API quota tradeoff varies by backend | Done |
| FR-SYNC-09 | System SHALL perform adaptive periodic reconciliation: start at configured interval (default 5 min), extend to longer intervals (up to 30 min) when no drift detected for N consecutive cycles, reset to base interval on drift detection | Must | Catches changes invisible to fsnotify. Adaptive interval reduces API quota usage by ~85% during stable periods while maintaining fast detection on drift. | Done (v0.5.0) |
| FR-SYNC-10 | System SHALL support immediate full sync via `sync-now` command | Must | User-initiated "sync everything now" is essential for trust | Done |
| FR-SYNC-11 | System SHALL support dry-run mode showing what would sync without executing | Must | Preview before commit; user error protection per 25010 | Done |
| FR-SYNC-12 | System SHALL record sync results (success/failure, timing, exit code) in state database | Must | Audit trail and debugging require persistent action history | Done |
| FR-SYNC-13 | System SHALL use signal-based adaptive cooldown: `cooldown = max(base * event_frequency_factor, last_sync_duration * 1.5)`. Cooldown is a function of both file temperature (event frequency) AND sync cost (file size / connection speed, measured as actual sync duration). No fixed constant. | Must | A fixed cooldown is universally wrong. A 100 MB file that took 45s to sync should not re-sync before ~67s. A hot auto-save file should converge to one sync per editing session. The signal is: "don't re-sync more often than it costs to sync." | Done (v0.5.0) |
| FR-SYNC-14 | System SHALL support per-mirror rclone extra flags (configured in mirrors[].rclone_extra_flags) | Must | Different backends need different flags (e.g., `--drive-chunk-size`, `--s3-storage-class`). Essential for multi-backend deployments. | Done (v0.5.0) |
| FR-SYNC-15 | System SHALL handle file renames as delete-old + sync-new | Must | Windows reports renames as two events; old remote path must be cleaned | Done |
| FR-SYNC-16 | System SHALL retry individual sync failures once (with short delay) before recording failure and engaging the circuit breaker | Should | Circuit breaker handles systemic failures; single-retry handles transient ones (momentary network glitch, temporary file lock). Without retry, a single timeout waits for the next reconciliation cycle. | Done (v0.5.0 — rclone exit 1/5 retried) |

### 3.4 Delete Handling (FR-DEL)

| ID | Requirement | Priority | Rationale | Status |
|----|------------|----------|-----------|--------|
| FR-DEL-01 | System SHALL support three delete policies as cleanly separated configuration profiles: `ignore`, `delete`, `quarantine`. No mixing — each profile activates a complete, self-contained code path. | Must | Clean separation prevents complexity leaks. Default is `delete`. | Done (`mirror` kept as deprecated alias as of v0.5.0) |
| FR-DEL-02 | `ignore` policy SHALL preserve remote files when local files are deleted | Must | Remote acts as append-only backup | Done |
| FR-DEL-03 | `delete` policy (default) SHALL immediately delete remote files when local files are deleted. Ghost cleanup also uses immediate delete. | Must | Code projects have git as safety net; clean exact mirror. | Done |
| FR-DEL-04 | `quarantine` policy SHALL move remote files to `.quarantine/<timestamp>/` on local delete. Ghost cleanup (orphans/leaks) also quarantined, not deleted. Fully self-contained mode. | Must | Recovery use case: documents, research, non-VCS content. All destructive operations go through quarantine when enabled. | Done |
| FR-DEL-05 | Quarantine retention SHALL be configurable (default 30 days) with auto-purge enforced during reconciliation | Must | Without enforcement, `quarantine_days` is a promise without delivery. Config field must be honored. | Done (v0.5.0) |
| FR-DEL-06 | Delete events SHALL be prioritized over sync events in the queue | Must | Stale remote files are a consistency hazard; deletes must not queue behind syncs | Done |
| FR-DEL-07 | Directory deletions with delete_policy=delete SHALL use `rclone purge` for atomic recursive deletion. Quarantine mode SHALL move children individually (each needs a timestamped path). | Must | `rclone purge` is a single API call on most backends vs O(N) deletefile calls. Atomic operation prevents partial-delete states. | Done (v0.5.0 with per-file fallback) |
| FR-DEL-08 | Rename cleanup SHALL force-delete old remote path regardless of delete policy | Must | Renames leave orphan at old path; force-delete is the only correct action | Done |
| FR-DEL-09 | System SHALL auto-purge quarantined files after retention period expires (checked during reconciliation cycle) | Should | Without auto-purge, quarantine grows unbounded on remote. Config field `quarantine_days` exists but is not enforced — a promise without delivery. | Done (v0.5.0) |

### 3.5 Ghost Cleanup (FR-GHOST)

| ID | Requirement | Priority | Rationale | Status |
|----|------------|----------|-----------|--------|
| FR-GHOST-01 | System SHALL detect orphan files (remote-only, no local counterpart, not excluded) | Must | Orphans waste remote storage and cause confusion | Done |
| FR-GHOST-02 | System SHALL detect leak files (remote files whose local source is now excluded) | Must | Filter change without cleanup violates user intent; unique to SelectiveMirror | Done |
| FR-GHOST-03 | `sync-now` SHALL clean up ghosts after sync completes | Must | Ghost cleanup is part of reaching consistent state | Done |
| FR-GHOST-04 | `dry-run` SHALL preview ghost cleanup without executing | Must | User error protection; preview before destructive action | Done |
| FR-GHOST-05 | System SHALL NOT delete quarantined files during ghost cleanup | Must | Quarantine is intentional; ghost cleanup must respect it | Done |
| FR-GHOST-06 | Periodic auto-verify SHALL detect and report ghosts without deleting | Should | Early warning without automated risk | Done |
| FR-GHOST-07 | System SHOULD report ghost counts in status output and metrics | Should | Observability for monitoring and alerting | Done |

### 3.6 Queue & Scheduling (FR-QUEUE)

| ID | Requirement | Priority | Rationale | Status |
|----|------------|----------|-----------|--------|
| FR-QUEUE-01 | System SHALL deduplicate repeated events for the same file (coalesce) | Must | Rapid saves produce N events; only the last matters | Done |
| FR-QUEUE-02 | System SHALL use move-to-back fairness (hot files cycle to tail, cold files advance) | Must | Without fairness, auto-saving files starve other projects | Done |
| FR-QUEUE-03 | System SHALL prioritize delete events (enqueue at head) | Must | Stale remote files are a consistency hazard (see FR-DEL-06) | Done |
| FR-QUEUE-04 | System SHALL enforce per-file cooldown after successful sync | Should | 30s cooldown prevents hot files from monopolizing API quota | Done |
| FR-QUEUE-05 | System SHALL implement circuit breaker with exponential backoff on consecutive per-mirror failures | Must | Prevents rapid retry loops on persistent failures (network, quota) | Done |
| FR-QUEUE-06 | Circuit breaker SHALL NOT block delete tasks | Must | Deletes are consistency-critical and must always execute | Done |
| FR-QUEUE-07 | Circuit breaker SHALL reset on first success | Must | Fast recovery when transient failure resolves | Done |
| FR-QUEUE-08 | System SHALL NOT impose an artificial queue size limit. Deduplication is the natural bound (max queue size = number of unique files across all mirrors). System SHALL log a warning when queue depth exceeds a configurable threshold (default 50K) and SHALL enforce a memory-based safety valve (default 200 MB). | Must | With dedup, 100K unique files = ~50 MB — acceptable. An artificial 10K limit causes event loss in legitimate burst scenarios (archive extraction, build output). The dedup *is* the bound. | Done (v0.5.0 — unbounded with 50K warning) |
| FR-QUEUE-09 | System SHOULD expose queue depth in metrics | Should | Capacity planning and bottleneck detection | Done |
| FR-QUEUE-10 | When queue depth warning threshold is exceeded, system SHALL trigger immediate reconciliation to accelerate draining | Should | High queue depth indicates the system is falling behind; reconciliation batch-processes files more efficiently than per-file queue processing | Not Done |

### 3.7 State Management (FR-STATE)

| ID | Requirement | Priority | Rationale | Status |
|----|------------|----------|-----------|--------|
| FR-STATE-01 | System SHALL maintain per-file sync state (hash, size, mtime, sync timestamp, exit code) in SQLite | Must | Enables checksum comparison, ghost detection, and audit trail | Done |
| FR-STATE-02 | System SHALL use WAL mode for concurrent read access | Must | Diagnostic commands must read DB while sync engine writes | Done |
| FR-STATE-03 | System SHALL enforce single-writer via MaxOpenConns=1 | Must | Multiple concurrent writers corrupt SQLite (SM-047) | Done |
| FR-STATE-04 | System SHALL log all sync actions (sync, delete, error) in a sync log table | Must | Audit trail for debugging and non-repudiation | Done |
| FR-STATE-05 | System SHALL store instance metadata (PID, executable, user, mode) | Should | Multi-instance debugging and status display | Done |
| FR-STATE-06 | System SHALL support schema migration for future versions | Should | Schema changes across upgrades must not require manual intervention | Partial (manual v2 migration) |
| FR-STATE-07 | System SHALL run integrity checks during diagnostics (`PRAGMA integrity_check`) | Should | Detect corruption before it causes data loss | Done |

### 3.8 Diagnostics & Health (FR-DIAG)

| ID | Requirement | Priority | Rationale | Status |
|----|------------|----------|-----------|--------|
| FR-DIAG-01 | `test-mirrors` SHALL run comprehensive diagnostics (config, paths, rclone, remote, state, filters, drift) | Must | Pre-flight validation prevents silent misconfiguration | Done |
| FR-DIAG-02 | `status` SHALL show sync metrics, instance state, and per-mirror health | Must | Operators need at-a-glance system health | Done |
| FR-DIAG-03 | `explain` SHALL show include/exclude status and matching rule for a given file | Must | #1 user debugging need: "why isn't this file syncing?" | Done |
| FR-DIAG-04 | `report-bug` SHALL generate a diagnostic bundle (sanitized config, env, metrics, recent logs) | Must | Bug reports without context waste maintainer time | Done |
| FR-DIAG-05 | System SHALL track health errors (panics, watcher errors) with bounded history (max 100) | Should | Post-mortem analysis of service behavior | Done |
| FR-DIAG-06 | System SHALL provide auto-verify at configurable intervals (default 6h) | Should | Catches drift from external changes or backend issues | Done |
| FR-DIAG-07 | System SHALL provide Windows toast notifications on drift/failure events | Should | Immediate user awareness without checking logs | Done |
| FR-DIAG-08 | System SHALL write machine-readable `status.json` for external monitoring | Should | Enables dashboard/alerting integration | Done |

### 3.9 Windows Service (FR-SVC)

| ID | Requirement | Priority | Rationale | Status |
|----|------------|----------|-----------|--------|
| FR-SVC-01 | System SHALL register/unregister as a Windows Service via SCM | Must | Unattended operation requires native service integration | Done |
| FR-SVC-02 | System SHALL auto-restart on failure (configurable recovery actions) | Must | Transient failures must self-heal without manual intervention | Done |
| FR-SVC-03 | System SHALL resolve rclone paths to absolute during service install | Must | SYSTEM account has different PATH; relative paths fail silently | Done |
| FR-SVC-04 | System SHALL handle SYSTEM account context (different PATH, APPDATA) | Must | SYSTEM has no rclone.conf in its APPDATA; critical production bug class | Done |
| FR-SVC-05 | System SHALL gracefully shut down on service Stop/Shutdown with 30s timeout | Must | In-flight syncs must complete or abort cleanly | Done |
| FR-SVC-06 | System SHALL write emergency crash logs to fixed paths for service diagnostics | Should | SYSTEM account failures are invisible without fixed-path logging | Done |
| FR-SVC-07 | System SHALL support `service install [start]`, `service stop [uninstall [--clean] [--yes]]` commands with compound sequencing | Must | Single-command install+start and stop+uninstall reduces operational friction | Done |
| FR-SVC-08 | System SHALL write service lifecycle events (start, stop, failure, recovery) to the Windows Event Log (Application log) | Should | Windows administrators expect service events in Event Viewer, not just text log files | Not Done |

### 3.10 CLI (FR-CLI)

| ID | Requirement | Priority | Rationale | Status |
|----|------------|----------|-----------|--------|
| FR-CLI-01 | System SHALL provide a single-binary CLI (`smirror.exe`) with subcommands | Must | Single binary = zero deployment complexity | Done |
| FR-CLI-02 | System SHALL support `--config` flag to override config file location | Must | Multiple configs for different environments (dev/prod) | Done |
| FR-CLI-03 | System SHALL provide `version` command showing version and build info | Must | Debugging and bug reports require version identification | Done |
| FR-CLI-04 | System SHALL provide `help` with usage information for all commands | Must | — | Done |
| FR-CLI-05 | System SHALL exit with non-zero code on errors | Must | Script/CI integration requires reliable exit codes | Done |
| FR-CLI-06 | System SHALL support optional mirror-name argument for targeted commands | Should | Operating on one mirror without affecting others | Done |
| FR-CLI-07 | System SHALL use documented exit codes: 0=success, 1=general error, 2=config error, 3=rclone error, 4=lock conflict, 5=drift detected, 6=upgrade declined | Must | Script and CI integration requires predictable, documented exit codes | Done |

### 3.11 Anomaly Detection & Reporting (FR-ANOM)

SelectiveMirror SHALL be a self-diagnosing system. Every anomaly (ghost, error, crash, drift) is a **signal** that something may be wrong with the system itself — not just a thing to handle. The anomaly reporter collects, correlates, and reports these signals for semi-automatic failure analysis.

| ID | Requirement | Priority | Rationale | Status |
|----|------------|----------|-----------|--------|
| FR-ANOM-01 | System SHALL classify runtime events into normal, warning, and anomalous categories | Must | Foundation for anomaly detection; not all events are equal | Done (v0.6.0) |
| FR-ANOM-02 | Anomalous events SHALL include: unexpected ghosts, repeated sync failures, circuit breaker activations, panic recovery, state DB integrity warnings, stale reconciliation, watcher errors, queue overflow | Must | These are signals of potential bugs or environmental failures | Done (v0.6.0 — 11 categories) |
| FR-ANOM-03 | Each anomaly SHALL be recorded with: timestamp, category, severity, affected mirror, affected file (if applicable), context snapshot (queue depth, recent errors, uptime) | Must | Root-cause analysis requires rich context at the moment of occurrence | Done (v0.6.0) |
| FR-ANOM-04 | System SHALL generate anomaly reports as structured JSON files in the data directory | Must | Machine-parseable for future automated analysis pipelines | Done (v0.6.0 — JSON-lines) |
| FR-ANOM-05 | Anomaly reports SHALL include a causal hypothesis chain (e.g., "orphan detected → was file renamed? recently excluded? never synced? externally deleted?") | Should | Guides semi-automatic failure analysis; turns raw events into investigable leads | Done (v0.6.0 — templates) |
| FR-ANOM-06 | System SHALL detect anomaly *patterns* (e.g., same file failing repeatedly, same mirror triggering circuit breaker daily, ghost count trending upward) | Should | Pattern detection elevates point-in-time events to systemic insights | Not Done |
| FR-ANOM-07 | System SHALL expose anomaly summary in `status` output and `status.json` | Must | Operators need visibility without reading raw report files | Done (v0.6.0) |
| FR-ANOM-08 | Anomaly reports SHALL be sanitized (no credentials, configurable path redaction) for safe sharing | Must | Reports may be shared with maintainers or automated systems | Done (v0.6.0 — home-dir redaction) |
| FR-ANOM-09 | System SHALL support configurable anomaly notification (toast notification; webhook in future) | Should | Active alerting vs passive log reading | Done (v0.6.0 toast + v0.8.x webhook) |
| FR-ANOM-10 | System SHALL auto-rotate anomaly reports (retain last 30 days, max 100 MB) | Should | Prevent unbounded growth of diagnostic data | Done (v0.6.0 — 30-day/50MB) |
| FR-ANOM-11 | Outbound anomaly reporting (to external endpoint) SHALL be a configurable feature: enabled by default when outbound internet is allowed, disabled otherwise. System SHALL emit zero outbound traffic when disabled. | Must | Not all deployments allow outbound traffic; user controls data flow | Done (v0.8.x — off by default; SSRF-safe webhook sender) |

#### Anomaly Categories

| Category | Trigger | Severity | Hypothesis Chain |
|----------|---------|----------|-----------------|
| **Ghost:Orphan** | Remote file, no local counterpart | Warning | Was file renamed? Deleted while service was down? External tool created it? Filter changed? |
| **Ghost:Leak** | Remote file, local source now excluded | Warning | When did filter change? Was sync-now run after? Is ghost cleanup working? |
| **SyncFailure:Repeated** | Same file fails > 3 times | Error | Is file locked? Permissions? Name invalid on target FS? Backend quota? |
| **CircuitBreaker:Activated** | 3+ consecutive mirror failures | Error | Network? Auth expired? Remote path deleted? Quota exceeded? |
| **Panic:Recovered** | safeGo caught a panic | Critical | Stack trace → code location → reproduce conditions |
| **StateDB:IntegrityWarning** | PRAGMA integrity_check fails | Critical | Disk corruption? Concurrent access? Power loss during write? |
| **Watcher:Error** | fsnotify error reported | Warning | Too many open handles? OS limit? Filesystem unmounted? |
| **Reconciliation:DriftDetected** | Files differ between local and remote after sync | Warning | Race condition? rclone bug? Backend eventual consistency? |
| **Reconciliation:Stale** | No reconciliation completed in 2x interval | Error | Worker starvation? Deadlock? rclone hanging? |
| **Queue:DepthWarning** | Queue depth exceeds configurable threshold | Warning | Burst event? Workers too slow? Backend down? |

---

## 4. Non-Functional Requirements (ISO/IEC 25010:2023)

### 4.0 Schema deviation note

SelectiveMirror's NFR organization in this section follows the **ISO/IEC 25010:2011 layout** (Usability as a top-level characteristic; Adaptability and Installability under Portability) rather than the **2023 layout** (Interaction Capability replaces Usability for non-interactive systems; Flexibility becomes top-level absorbing Adaptability/Installability/Scalability/Replaceability; Safety becomes a top-level characteristic).

The substantive engineering content satisfies the 2023 model; only the section taxonomy differs. Mapping to 2023 sub-characteristics:

| 2023 top-level / sub-characteristic | Where in this SRS | Status |
|---|---|---|
| Functional Suitability:Functional Appropriateness | implicit in `FR-DEL-01` three-policy design | ⚠️ not declared as NFR |
| Interaction Capability | §4.4 Usability (re-labeled in 2023; non-interactive product) | ✅ identical content; deviation is label-only |
| Reliability:Faultlessness | implicit in NFR-FT-* (renamed from Maturity in 2023) | ⚠️ no explicit faultlessness metric |
| Security:Authenticity / Resistance / Privacy (new in 2023) | not yet declared | ❌ — committed for v1.1 |
| Maintainability:Reusability / Analysability | hook surface (FR-ASP-17) implies Reusability; FR-DIAG/FR-ANOM imply Analysability | ⚠️ not declared as NFRs |
| Flexibility:Adaptability | §4.8.1 (under Portability) | ✅ |
| Flexibility:Installability | §4.8.2 (under Portability) | ✅ |
| Flexibility:Replaceability | rclone-as-transport doctrine §2.5; not a separate NFR | ⚠️ not declared |
| Flexibility:Scalability | §4.2.3 Capacity | ⚠️ not separately declared |
| Safety | N/A — formal justification: SelectiveMirror is not a safety-critical system. Data-loss scenarios (`FR-DEL-03` default `delete`, `FR-DEL-08` rename force-delete) are mitigated by the `quarantine` policy option (`FR-DEL-04`) and by VCS integration in code-project use cases. No Safety NFRs declared. | ➖ |

**Migration plan**: Full restructure to 2023 layout is committed for **v1.1** (action `A-25010-01b` in `docs/iso-compliance.md` §9.2). For v1.0, this deviation note is the authoritative pointer. Compliance status against 25010:2023 is tracked in `docs/iso-compliance.md` §5.

---

This section is organized by ISO/IEC 25010 quality characteristics (2011 layout — see §4.0 above for 2023 mapping). Each sub-characteristic maps to measurable requirements per ISO/IEC 25023. Requirements use 29148 attributes: ID, statement, rationale, target, measurement method, and status.

### 4.1 Functional Suitability

*Degree to which the product provides functions that meet stated and implied needs.*

#### 4.1.1 Functional Completeness

| ID | Requirement | Rationale | Target | Status |
|----|------------|-----------|--------|--------|
| NFR-FS-01 | All functional requirements in Section 3 marked "Must" SHALL be implemented before v1.0 release | Core value proposition depends on complete feature set | 100% of Must requirements | ~99% (all Must-tier FRs delivered through v0.8.x; FR-WATCH-10 USN journal remains, scheduled for Phase 3) |
| NFR-FS-02 | `test-mirrors` SHALL detect all common misconfiguration scenarios before first sync | Users must trust setup validation | 13+ diagnostic checks | Met |

#### 4.1.2 Functional Correctness

| ID | Requirement | Rationale | Target | Measurement | Status |
|----|------------|-----------|--------|-------------|--------|
| NFR-FC-01 | Sync operations SHALL produce byte-identical remote copies of local files | Data integrity is the primary contract | Zero checksum mismatches post-sync | `rclone check --checksum` in verify | Met |
| NFR-FC-02 | Filter evaluation SHALL match `.gitignore` specification (last-match-wins, negation, directory markers) | Filter accuracy is a differentiator; incorrect filters cause data loss or leaks | Zero false-include, zero false-exclude vs gitignore reference impl | SM-036/037 regression tests | Met |
| NFR-FC-03 | Ghost classification SHALL correctly distinguish orphans from leaks | Misclassification could delete wanted files | Zero misclassification | SM-053/055 regression tests | Met |

### 4.2 Performance Efficiency

*Performance relative to the amount of resources used under stated conditions.*

#### 4.2.1 Time Behaviour

| ID | Requirement | Rationale | v1.0 SLA | Measurement | Status |
|----|------------|-----------|----------|-------------|--------|
| NFR-TB-01 | File change detection latency (filesystem event to queue entry) | Users expect near-real-time response. **Target revised v1.1 (SM-153 path 1)**: 50 ms p99 was aspirational; fsnotify on Windows is event-driven but p99 under load is dominated by goroutine scheduling. 100 ms p99 is realistic and competitive with Syncthing/OneDrive. | < 100ms (p99) | Instrumented event timestamps | Met |
| NFR-TB-02 | Single-file sync latency, small files (queue dequeue to rclone exit, < 1 MB) | Core UX metric — how fast changes appear on remote. **Target revised v1.1 (SM-153 path 1)**: 3 s p95 assumed broadband; 5 s p95 covers slower connections and metered links. | < 5s (p95) on broadband | Sync log duration_ms column | Met |
| NFR-TB-03 | Single-file sync latency, large files (< 100 MB) | Large files should complete within a minute | < 60s (p95) on broadband | Sync log duration_ms column | Not Measured |
| NFR-TB-04 | Startup reconciliation (batch sync, no-change case) | Long startup blocks service availability | < 30s for 4 mirrors, < 10,000 total files | Manual timing; CI benchmark planned | Met |
| NFR-TB-05 | Startup reconciliation with USN journal (when available) | USN journal eliminates full scan on restart | < 5s | USN query timing | Planned (Phase 3) |
| NFR-TB-06 | Queue throughput (sustained dequeue rate) | Must exceed peak filesystem event rates | > 100 events/second | Benchmark test | Not Measured |
| NFR-TB-07 | Service restart total time (graceful shutdown + reconciliation) | Minimal disruption window | < 10s shutdown + < 30s reconciliation = < 40s total | End-to-end timing | Not Measured |

#### 4.2.2 Resource Utilization

| ID | Requirement | Rationale | v1.0 SLA | Measurement | Status |
|----|------------|-----------|----------|-------------|--------|
| NFR-RU-01 | Memory usage — idle (4 mirrors, no pending events) | Long-running service must not bloat | < 25 MB RSS | Task Manager / process stats. Competitive: Syncthing ~20 MB idle | **Not Met** (currently 30 MB; 25 MB target retained as v1.1 optimization goal — see SM-153 path 2) |
| NFR-RU-02 | Memory usage — loaded (10K queued events) | Burst scenarios must not OOM | < 80 MB RSS | Stress test. 10K Tasks at ~500 bytes = 5 MB queue + overhead | Not Measured |
| NFR-RU-03 | CPU usage — idle (watching, no sync activity) | Service must be invisible to user. **Target revised v1.1 (SM-153 path 1)**: 0.5% required Go-runtime tuning not justified by user impact; 1% sustained is industry-acceptable for a watching-idle process. | < 1% sustained CPU | Performance monitor | Met |
| NFR-RU-04 | Disk I/O — state DB writes per sync cycle | SQLite must not become I/O bottleneck | < 10 IOPS per file sync | SQLite stats | Not Measured |
| NFR-RU-05 | Reconciliation API calls per cycle | Batch operations only; never per-file remote listing | O(mirrors), not O(files) | rclone invocation count per reconciliation | Met |

#### 4.2.3 Capacity

| ID | Requirement | Rationale | v1.0 SLA | Measurement | Status |
|----|------------|-----------|----------|-------------|--------|
| NFR-CA-01 | Maximum concurrent mirrors without degradation | Power users and enterprise deployments | 32 mirrors | Load test (latency, CPU, memory under load) | Not Tested |
| NFR-CA-02 | Maximum tracked files per mirror | Enterprise-scale monorepos | 100,000 files per mirror | State DB performance test | Not Tested |
| NFR-CA-03 | Maximum queue depth | Natural bound via dedup (see FR-QUEUE-08) | Unbounded (dedup-limited); memory safety valve at 200 MB | Queue memory profiling | Done (v0.5.0 — unbounded with 50K warning) |

### 4.3 Compatibility

*Degree to which a product can exchange information with other products and perform its functions while sharing the same environment.*

#### 4.3.1 Co-existence

| ID | Requirement | Rationale | Target | Status |
|----|------------|-----------|--------|--------|
| NFR-CX-01 | System SHALL coexist with other file-watching tools (IDEs, antivirus, backup agents) without interference | Users run many tools on same directories | Zero mutual interference | Met |
| NFR-CX-02 | System SHALL NOT exclusively lock watched files or directories | Exclusive locks break editors and build tools | Shared-read access only | Met |
| NFR-CX-03 | Log files SHALL be readable by external tools while smirror is running | PowerShell `Get-Content -Wait` must work | FILE_SHARE_READ\|WRITE\|DELETE on Windows | Met |

#### 4.3.2 Interoperability

| ID | Requirement | Rationale | Target | Status |
|----|------------|-----------|--------|--------|
| NFR-IO-01 | System SHALL interoperate with any rclone-supported backend (70+) | Backend-agnostic is a core differentiator | All backends supported by rclone v1.73+ | Met (tested: Google Drive, local) |
| NFR-IO-02 | System SHALL produce machine-readable status output (`status.json`) | External monitoring and automation integration | JSON schema documented | Met |
| NFR-IO-03 | Filter syntax SHALL be compatible with `.gitignore` specification | Developers already know gitignore; zero learning curve | Passes gitignore test corpus | Met |

### 4.4 Usability

*Degree to which a product can be used by specified users to achieve specified goals with effectiveness, efficiency, and satisfaction.*

#### 4.4.1 Learnability

| ID | Requirement | Rationale | Target | Status |
|----|------------|-----------|--------|--------|
| NFR-LN-01 | First-time setup SHALL require exactly: install rclone, create config, run `smirror start` | Low barrier to adoption | 3-step setup, < 10 min for experienced CLI user | Met |
| NFR-LN-02 | Configuration SHALL use sensible defaults requiring only `mirrors` section | Minimize required knowledge | 3-line minimal config (name, local_path, remote) | Met |
| NFR-LN-03 | Annotated example config SHALL document all options with defaults | Self-documenting configuration | config.example.yaml complete | Met |

#### 4.4.2 Operability

| ID | Requirement | Rationale | Target | Status |
|----|------------|-----------|--------|--------|
| NFR-OP-01 | Error messages SHALL be actionable (what went wrong + how to fix) | Reduces support burden; respects user's time | All error paths include remediation hint | Partial |
| NFR-OP-02 | `explain` command SHALL show filter decision + matching rule for any file | Debugging filter behavior is the #1 user pain point | Single command, instant result | Met |
| NFR-OP-03 | `report-bug` SHALL produce a self-contained diagnostic bundle | Bug reports without diagnostics waste maintainer time | Config + env + metrics + recent logs in one output | Met |

#### 4.4.3 User Error Protection

| ID | Requirement | Rationale | Target | Status |
|----|------------|-----------|--------|--------|
| NFR-UE-01 | Default delete policy SHALL be `delete` (mirror local deletions to remote). Deletion safety is provided by the `delete_policy: quarantine` option for non-VCS content and by git as the safety net for code projects. | Code projects have git as safety net; an archive-preserving default was considered (`ignore`) but rejected because it creates silent remote-local divergence for the typical user. Users whose workflows require archive semantics set `delete_policy: ignore` or `quarantine` explicitly. | Default = delete | Met |
| NFR-UE-02 | `test-mirrors` SHALL validate configuration before first sync can run | Catch misconfigs before they cause damage | 13+ checks including remote reachability | Met |
| NFR-UE-03 | `dry-run` SHALL exist for every destructive operation (sync, ghost cleanup) | Users must be able to preview before committing | Full preview with no side effects | Met |
| NFR-UE-04 | Single-instance lock SHALL prevent concurrent processes from corrupting state | Two instances = guaranteed corruption | File-based lock with stale detection | Met |

### 4.5 Reliability

*Degree to which a system performs specified functions under specified conditions for a specified period of time.*

#### 4.5.1 Fault Tolerance

| ID | Requirement | Rationale | Target | Measurement | Status |
|----|------------|-----------|--------|-------------|--------|
| NFR-FT-01 | System SHALL recover from rclone process failures without crashing | rclone errors are transient (network, quota, permission) | Zero service crashes from rclone failures | Circuit breaker + error logging. **v0.9.12-dev**: rclone-stall detection (`internal/sync/liveness.go`) adds Layer-2 multi-signal stall backstop — `Sync:Stalled` anomaly (warning) when ALL of {output / cpu_time / io_bytes} signals are flat for K consecutive ticks; thresholds: transfer ops 10s × K=6 = 60s flat-grace, metadata ops 30s × K=8 = 240s. Replaces brittle 5-min wall-clock timeout. See `docs/rclone-stall-design-for-review.md`. | Met |
| NFR-FT-02 | System SHALL isolate panics in any goroutine without crashing the service | Go panics in workers must not take down the service | safeGo wrapper on all goroutines | Health error tracking (max 100) | Met |
| NFR-FT-03 | Circuit breaker SHALL back off exponentially after consecutive mirror failures | Prevent rapid retry loops on persistent failures | 3-failure threshold, 10s base, 5min cap | Per-mirror state tracking | Met |
| NFR-FT-04 | System SHALL detect and recover from dropped filesystem events | OS event buffer overflow causes silent data loss | Reconciliation within configurable interval (default 5 min) | Periodic batch sync | Met |

#### 4.5.2 Recoverability

| ID | Requirement | Rationale | Target | Measurement | Status |
|----|------------|-----------|--------|-------------|--------|
| NFR-RC-01 | State database SHALL maintain integrity across process crashes | Unclean shutdown must not corrupt sync state | SQLite WAL mode + single writer (MaxOpenConns=1) | `PRAGMA integrity_check` in test-mirrors | Met |
| NFR-RC-02 | Windows service SHALL auto-restart on failure | Unattended operation demands self-healing | 3 restart attempts (10s, 30s, 60s), reset after 24h | SCM recovery actions | Met |
| NFR-RC-03 | Emergency crash logs SHALL be written to fixed paths for service diagnostics | SYSTEM account failures are invisible without fixed-path logging | `C:\smirror-early.log`, `C:\smirror-service-crash.log` | File existence after crash | Met |
| NFR-RC-04 | System SHALL reconcile state on startup (full batch sync per mirror) | Catches all changes missed during downtime | One rclone copy per mirror on start | Startup timing in logs | Met |

#### 4.5.3 Availability

| ID | Requirement | Rationale | Target | Measurement | Status |
|----|------------|-----------|--------|-------------|--------|
| NFR-AV-01 | System uptime target as Windows service | Long-running service SLA | 99.9% (< 8.7h unplanned downtime/year) | Service event log monitoring | Not Measured |
| NFR-AV-02 | Diagnostic commands SHALL work while sync engine is running | Operators must observe without disrupting sync | status, explain, test-mirrors all non-blocking | Shared DB access (WAL) | Met |

### 4.6 Security

*Degree to which a product protects information and data.*

#### 4.6.1 Confidentiality

| ID | Requirement | Rationale | Target | Status |
|----|------------|-----------|--------|--------|
| NFR-CO-01 | System SHALL NOT store, transmit, or log credentials | Credential leaks in logs/DB would be a critical vulnerability | Zero credential exposure in all outputs | Met |
| NFR-CO-02 | System SHALL reference rclone config by path only (never read or modify its contents) | rclone.conf contains OAuth tokens; smirror must not access them | Path-only reference | Met |
| NFR-CO-03 | Diagnostic reports (`report-bug`) SHALL sanitize sensitive paths and config values | Bug reports may be shared publicly | Sanitized output | Partial |
| NFR-CO-04 | Telemetry SHALL never transmit PII, file names, paths, or credentials | Privacy is non-negotiable for opt-in trust | Zero PII in telemetry payload | Planned |

#### 4.6.2 Integrity

| ID | Requirement | Rationale | Target | Status |
|----|------------|-----------|--------|--------|
| NFR-IN-01 | Sync operations SHALL use checksums to verify data integrity | Bit-rot or truncated uploads must be detectable | MD5 checksum comparison (rclone --checksum) | Met |
| NFR-IN-02 | Single-instance lock SHALL prevent concurrent state DB access | Concurrent writers corrupt SQLite | File-based lock (LockFileEx/flock) | Met |
| NFR-IN-03 | State DB schema version SHALL be tracked to prevent incompatible access | Schema mismatch after upgrade could corrupt data | Version column in metadata | Partial |

#### 4.6.3 Non-repudiation

| ID | Requirement | Rationale | Target | Status |
|----|------------|-----------|--------|--------|
| NFR-NR-01 | All sync actions SHALL be logged in the sync_log table with timestamp and result | Audit trail for "what happened to my files" | Every sync/delete/error recorded | Met |
| NFR-NR-02 | Instance metadata SHALL record PID, executable path, user, and mode | Identify which process/user caused actions | Stored in state DB metadata table | Met |

#### 4.6.4 Accountability

| ID | Requirement | Rationale | Target | Status |
|----|------------|-----------|--------|--------|
| NFR-AC-01 | System SHALL NOT open network listeners | Minimizes attack surface; outbound-only via rclone | Zero inbound ports | Met |
| NFR-AC-02 | System SHALL prevent directory traversal via symlink-to-directory rejection | Symlink escape could sync files outside project boundary | Reject symlink-to-dir in watcher setup | Met |

### 4.7 Maintainability

*Degree of effectiveness and efficiency with which a product can be modified.*

#### 4.7.1 Modularity

| ID | Requirement | Rationale | Target | Status |
|----|------------|-----------|--------|--------|
| NFR-MO-01 | Platform-specific code SHALL be isolated in `_windows.go` / `_unix.go` build-tagged files | Adding platforms must not require modifying core logic | All platform code behind build tags | Met |
| NFR-MO-02 | Each internal package SHALL have a single responsibility | Packages must be independently testable and replaceable | config, filter, watcher, sync, state, lock, metrics, logging, notify, rclone, service, anomaly, hooks, telemetry | Met (14 packages) |

#### 4.7.2 Testability

| ID | Requirement | Rationale | Target | Measurement | Status |
|----|------------|-----------|--------|-------------|--------|
| NFR-TE-01 | Unit test count SHALL exceed 280 across all packages | Regression safety net for rapid development | > 280 tests | `go test ./internal/... ./cmd/... -count=1` | Met (608 tests across 15 packages; total internal/ statement coverage **66.6%** — measured 2026-04-27, above v1.0 60% target). Watcher at 59.3% (was reported as 16.6% in `VV-Plan.md` §5.2 — that baseline was 9 days stale; see SM-155). Full watcher refactor for testability (X-04 in `docs/iso-compliance.md`) deferred to v1.0.1; v1.0 ships at current 59.3% with no further blockers. |
| NFR-TE-02 | All tests SHALL pass with `-race` detection | Concurrency bugs are the hardest class to reproduce | Zero data races | `go test -race` on concurrent packages | Met |
| NFR-TE-03 | Critical subsystems SHALL have injectable dependencies for test isolation | Tests must not require rclone or network | ListRemoteFunc, syncer interface | Met |
| NFR-TE-04 | Bug hunt markers (SM-xxx) SHALL link each regression test to the bug it covers | Traceability from test to requirement to fix | Every SM-xxx test documents the scenario | Met |

#### 4.7.3 Modifiability

| ID | Requirement | Rationale | Target | Status |
|----|------------|-----------|--------|--------|
| NFR-MD-01 | Configuration SHALL validate at startup with clear error messages | Bad config should fail fast, not at sync time | All config fields validated in config.Load() | Met |
| NFR-MD-02 | CHANGELOG SHALL document every user-visible change | Contributors and users need change history | Complete from v0.2.8 through current | Met |
| NFR-MD-03 | Structured logging via slog with component tags SHALL be used throughout | Grep-friendly logs for debugging | All log calls include "component" field | Met |
| NFR-MD-04 | State DB schema SHALL support forward migration | Schema changes must not require manual intervention | Auto-migration framework | Partial (manual v2 only) |

### 4.8 Portability

*Degree of effectiveness and efficiency with which a system can be transferred from one environment to another.*

#### 4.8.1 Adaptability

| ID | Requirement | Rationale | Target | Status |
|----|------------|-----------|--------|--------|
| NFR-AD-01 | Core sync engine SHALL compile on Linux and macOS (native build) | Future cross-platform support must be architecturally possible | Native build on target platform with CGo (cross-compile from Windows is no longer supported since SM-148 moved to mattn/go-sqlite3) | Partial (build tags exist, untested at runtime) |
| NFR-AD-02 | Filename handling SHALL not assume NTFS semantics | Source FS may be ext4, exFAT, ZFS in future; target FS varies by backend | FS-agnostic path construction | Partial |
| NFR-AD-03 | Binary SHALL be statically linked with no runtime dependencies beyond rclone | Deployment must be copy-and-run | CGo enabled with statically-linked mattn/go-sqlite3 (SQLite C is embedded); no DLL/shared-library dependencies at runtime | Met |

#### 4.8.2 Installability

| ID | Requirement | Rationale | Target | Status |
|----|------------|-----------|--------|--------|
| NFR-IS-01 | System SHALL be installable via MSI, ZIP, or `go install` | Multiple installation paths for different user types | MSI (WiX), ZIP (GoReleaser), source build | Met |
| NFR-IS-02 | MSI installer SHALL offer to install rclone if not present | rclone is a prerequisite; installer should help | Post-install custom action (install-rclone.ps1) | Met |
| NFR-IS-03 | Service installation SHALL auto-resolve paths for SYSTEM account | SYSTEM account has different PATH/APPDATA | `service install` resolves rclone_path and rclone_config | Met |

---

## 5. Interface Requirements

### 5.1 User Interfaces

SelectiveMirror is CLI-only. No GUI exists or is planned for the core product.

| Interface | Format | Notes |
|-----------|--------|-------|
| CLI output | Plain text, color-coded (ANSI) | Human-readable terminal output |
| `status.json` | JSON | Machine-readable for dashboards/scripts |
| Config file | YAML | Annotated example provided |
| Log file | slog text format | Rotating, shared-access on Windows |

### 5.2 System Interfaces

| Interface | Protocol | Notes |
|-----------|----------|-------|
| rclone | Subprocess (stdin/stdout/stderr, exit codes) | All remote I/O delegated |
| Filesystem | ReadDirectoryChangesW (Windows), fsnotify (cross-platform) | Event-driven change detection |
| SQLite | database/sql with `github.com/mattn/go-sqlite3` driver (CGo; SQLite C statically linked into the binary) | Local state persistence |
| Windows SCM | golang.org/x/sys/windows/svc | Service lifecycle |
| Toast notifications | Windows notification API | Optional, rate-limited |

### 5.3 External Interfaces

| Interface | Direction | Notes |
|-----------|-----------|-------|
| Cloud backends | Outbound only (via rclone) | Never direct — always through rclone subprocess |
| GitHub Releases API | Outbound (planned) | Update check via telemetry module |
| Telemetry endpoint | Outbound (planned, opt-in) | Anonymous usage stats |

---

## 6. Data Requirements

### 6.1 State Database Schema

| Table | Purpose | Key Columns |
|-------|---------|-------------|
| `file_state` | Per-file sync tracking | mirror, rel_path, hash, size, mtime_ns, synced_at, exit_code |
| `sync_log` | Audit trail of all sync operations | timestamp, mirror, rel_path, action, result, duration_ms |
| `metadata` | Key-value store for instance info | key, value (PID, mode, health_errors, ghost_scan, etc.) |

### 6.2 Configuration Data

- Primary: `~/.selectivemirror/config.yaml`
- Per-project: `<mirror_root>/.syncignore`
- Lock: `<config_dir>/smirror.lock`

### 6.3 Data Retention

| Data | Retention | Notes |
|------|-----------|-------|
| State DB | Indefinite (grows with file count) | ~1 KB per tracked file |
| Sync log | Indefinite | Could grow large; no auto-pruning (gap) |
| Log files | 5 x 10 MB (50 MB max) | Rotating |
| Quarantine | Configurable (default 30 days) | Manual cleanup only (auto-purge not implemented) |
| Status.json | Overwritten each heartbeat | Single file |

---

## 7. Constraints & Assumptions

### 7.1 Constraints

1. **rclone dependency** — All remote operations require rclone v1.73+ installed and configured
2. **Single direction** — One-way local-to-remote only. Bidirectional sync is permanently out of scope; it can be composed from two independent SelectiveMirror instances targeting non-overlapping directories
3. **Single instance** — Only one smirror process per config file
4. **Windows-primary** — Service mode is Windows-only; foreground mode compiles cross-platform but is untested
5. **No native backends** — Cannot operate without rclone (no direct API calls to cloud providers)
6. **SQLite single writer** — State DB throughput limited by single connection

### 7.2 Assumptions

1. rclone handles all backend-specific authentication, rate limiting, and retry logic
2. File saves are atomic or produce detectable mtime changes during writes
3. Users configure rclone remotes independently before using SelectiveMirror
4. Filesystem event delivery is best-effort (reconciliation compensates for gaps)
5. Target remote supports rclone's `copyto`, `deletefile`, `lsjson` commands

---

## 8. Future Requirements

### 8.1 Phase 3: USN Journal Recovery (Planned)

| ID | Requirement | Priority |
|----|------------|----------|
| FR-USN-01 | System SHALL read the NTFS USN journal on startup to detect changes that occurred while stopped | Must |
| FR-USN-02 | System SHALL use USN sequence numbers to identify the exact gap between shutdown and restart | Must |
| FR-USN-03 | System SHALL fall back to full reconciliation if USN journal is unavailable or rolled over | Must |

### 8.2 Phase 5: Telemetry (Planned)

| ID | Requirement | Priority |
|----|------------|----------|
| FR-TEL-01 | System SHALL support opt-in anonymous telemetry (disabled by default) | Must |
| FR-TEL-02 | System SHALL emit zero outbound traffic when telemetry is disabled | Must |
| FR-TEL-03 | System SHALL never transmit PII, file names, paths, or credentials | Must |
| FR-TEL-04 | System SHALL support update-available notifications via GitHub Releases API | Should |

### 8.3 Committed for v1.0 (Post-0.4.0)

These requirements are agreed for implementation before v1.0 release.

| ID | Requirement | Rationale | Phase |
|----|------------|-----------|-------|
| FR-ASP-06 | **Per-mirror delete policy** — Different delete policies per mirror (configured in mirrors[].delete_policy) | Code projects use `mirror` (have git); documents use `quarantine` (no VCS safety net). Different content types have different risk profiles. | v0.5.0 |
| FR-ASP-11 | **Sync log pruning** — Retain 30 days of detail, aggregate older entries into daily summaries | 1,000 files/day = 365K rows/year. Unbounded growth is a production reliability risk. | v0.5.0 |
| FR-ASP-16 | **State DB auto-migration framework** — Versioned schema with automatic upgrade on startup | Manual migration is a landmine for upgrades. Users who upgrade the binary hit schema mismatch. Table-stakes for any product with a state DB. | v0.5.0 |
| FR-ASP-17 | **Pre/post-sync hook system** — Configurable shell commands triggered before and after sync events | Enables validation (lint before upload), transformation (compress), notification (Slack, CI trigger), and orchestration. This is how SelectiveMirror becomes a platform, not just a tool. Must be designed early so callback points are ready. | v0.6.0 |

### 8.4 Committed Post-v1.0

| ID | Requirement | Rationale |
|----|------------|-----------|
| FR-ASP-05 | **Web dashboard / tray icon** — Visual status, pause/resume, per-mirror control | Intentionally deferred past v1.0: mature the feature set via CLI user feedback first, then build GUI for the stable feature surface. Same sequencing as Docker CLI → Docker Desktop. |
| FR-ASP-08 | **Cross-platform service mode** — systemd (Linux), launchd (macOS), and other OS/filesystem support | Build-tag architecture already supports this. Post-v1.0 to keep first release focused. Scope: any OS with rclone support. |
| FR-ASP-14 | **Multi-destination per mirror** — Sync one local dir to multiple remotes simultaneously | Strong DR/redundancy use case. High complexity (failure handling, partial success). Post-v1.0. |

### 8.5 Aspirational (Not Committed)

Candidates for future consideration — not committed to any release.

| ID | Requirement | Rationale |
|----|------------|-----------|
| FR-ASP-03 | **File versioning / snapshots** — Keep N previous versions on remote | Safety net beyond quarantine; some backends support natively |
| FR-ASP-04 | **Selective restore** — Pull specific files/versions from remote to local | Complements versioning; currently requires manual rclone |
| FR-ASP-07 | **Per-mirror bandwidth allocation** — Bandwidth budget per mirror, not just global | Prevents one large mirror from starving others |
| FR-ASP-09 | **Encrypted-at-rest sync** — Client-side encryption before upload (via rclone crypt) | Privacy-sensitive data mirroring |
| FR-ASP-10 | **Webhook/API on sync events** — Notify external systems on sync completion | Integration with CI/CD, chat, or orchestration tools (partially addressed by FR-ASP-17 hooks) |
| FR-ASP-13 | **Config hot-reload** — Detect config.yaml changes and apply without restart | Adding/removing mirrors without service restart |
| FR-ASP-15 | **Transfer progress reporting** — Show upload progress for large files | UX improvement for large-file workflows |
| FR-ASP-18 | **Bandwidth scheduling** — Time-of-day bandwidth policies | Avoids network contention during business hours |
| FR-ASP-19 | **Initial seed from existing remote** — Skip uploading files already present on remote | Faster onboarding when remote already has partial data |

**Removed from scope**:
- ~~FR-ASP-01 Bidirectional sync~~ — Out of scope permanently. Composable from two one-directional instances targeting non-overlapping directories (Section 7.1).
- ~~FR-ASP-02 Conflict resolution~~ — Only needed for bidirectional sync, which is out of scope.

---

## 9. Competitive Gap Analysis

### 9.1 Landscape

| Tool | Real-time | Selective | Backend-agnostic | Service mode | Single binary | Delete policy |
|------|-----------|-----------|------------------|-------------|---------------|---------------|
| **SelectiveMirror** | Yes | Yes (.gitignore) | Yes (70+ via rclone) | Yes (Windows) | Yes | 3 modes |
| Google Drive Desktop | Yes | No | No (Drive only) | Yes | Yes | Mirror only |
| Dropbox | Yes | Selective sync (folder-level) | No (Dropbox only) | Yes | Yes | Mirror only |
| OneDrive | Yes | Yes (folder-level) | No (OneDrive only) | Yes | Yes | Mirror only |
| Syncthing | Yes | Yes (patterns) | No (peer-to-peer) | Yes | Yes | Trash/ignore |
| rclone bisync | Manual/cron | Yes (filters) | Yes | No | Yes | Mirror only |
| Resilio Sync | Yes | Yes (patterns) | No (peer-to-peer) | Yes | Yes | Trash |
| FreeFileSync | Manual/schedule | Yes (filters) | No (local/SFTP/FTP/Drive) | No | Yes | Versioning |

### 9.2 SelectiveMirror's Differentiators

1. **`.gitignore`-syntax filtering** — Most tools use proprietary filter formats or folder-level selection
2. **70+ backends via rclone** — No other real-time watcher supports this breadth
3. **Three delete policies** — Most tools only offer mirror (delete remote) or ignore
4. **Ghost detection and cleanup** — Unique: detects leaks (filter changed) and orphans
5. **Circuit breaker + FairQueue** — Production-grade scheduling absent from comparable tools
6. **Diagnostic suite** — `test-mirrors`, `explain`, `report-bug` — developer-grade observability

### 9.3 Gaps vs. Competition

| Gap | Who Has It | Impact |
|-----|-----------|--------|
| No bidirectional sync | Syncthing, Dropbox, OneDrive, rclone bisync | Users needing pull-from-remote must use rclone directly |
| No GUI/tray icon | Every consumer sync tool | Barrier for non-technical users |
| No conflict resolution | Syncthing (rename-both), Dropbox (conflicted copy) | Not needed unless bidirectional added |
| No file versioning | FreeFileSync, Dropbox (30/180 days) | Quarantine is partial substitute |
| Windows-only service | Syncthing (all platforms), Resilio (all platforms) | Limits Linux/macOS server use cases |
| No mobile client | Syncthing, Dropbox, OneDrive | Not in scope (cloud access via remote) |
| No transfer progress | Most sync tools show progress | UX gap for large files |

---

## 10. Appendices

### 10.1 Requirement Priority Key

| Priority | Definition |
|----------|-----------|
| **Must** | Required for the system to be functional. Blocking for release. |
| **Should** | Expected by users. Omission is a known gap. |
| **Could** | Nice to have. Deferred if resource-constrained. |
| **Won't** | Explicitly out of scope for current version. |

### 10.2 ISO/IEC 25010 Quality Characteristics Key

Section 4 is organized by these quality characteristics:

| Characteristic | Sub-characteristics Used | Section |
|---------------|------------------------|---------|
| **Functional Suitability** | Completeness, Correctness | 4.1 |
| **Performance Efficiency** | Time Behaviour, Resource Utilization, Capacity | 4.2 |
| **Compatibility** | Co-existence, Interoperability | 4.3 |
| **Usability** | Learnability, Operability, User Error Protection | 4.4 |
| **Reliability** | Fault Tolerance, Recoverability, Availability | 4.5 |
| **Security** | Confidentiality, Integrity, Non-repudiation, Accountability | 4.6 |
| **Maintainability** | Modularity, Testability, Modifiability | 4.7 |
| **Portability** | Adaptability, Installability | 4.8 |

Not applied (not relevant to this product):
- **Interaction Capability** (25010 replaces Usability for interactive systems) — SelectiveMirror is non-interactive (CLI + service)
- **Flexibility** — Covered implicitly by Modifiability and Adaptability
- **Safety** — Not a safety-critical system

### 10.3 Status Key

| Status | Definition |
|--------|-----------|
| **Done** | Implemented, tested, and passing |
| **Partial** | Partially implemented or has known gaps |
| **Planned** | Committed to a specific phase |
| **Not Done** | Identified as needed but not yet started |
| **Not Measured** | Requirement exists but no measurement taken |
| **Not Tested** | Implementation exists but not validated at scale |

### 10.4 Traceability (per ISO/IEC/IEEE 29148:2018)

#### 10.4.1 Traceability Dimensions

Each requirement is traceable across four axes:

| Axis | Source | Lookup Method |
|------|--------|---------------|
| **Requirement → Source code** | `internal/` packages | Requirement ID in code comments or function names |
| **Requirement → Test** | `*_test.go` files | SM-xxx markers link regression tests to bugs/requirements |
| **Requirement → Version** | `CHANGELOG.md` | Version where requirement was first implemented |
| **Requirement → Phase** | This SRS, Section 8 | Roadmap phase that scoped the requirement |

#### 10.4.2 Requirement-to-Package Map

| Requirement Group | Primary Package(s) | Test File(s) |
|-------------------|-------------------|-------------|
| FR-WATCH | `internal/watcher/` | `watcher_test.go` |
| FR-FILTER | `internal/filter/` | `filter_test.go` |
| FR-SYNC | `internal/sync/` | `sync_test.go` |
| FR-DEL | `internal/sync/` (deleteRemoteFile, deleteRemoteDir) | `sync_test.go` |
| FR-GHOST | `internal/sync/` (findGhosts, CleanupGhosts) | `sync_test.go` |
| FR-QUEUE | `internal/sync/fairqueue.go` | `sync_test.go` (FairQueue tests) |
| FR-STATE | `internal/state/` | `state_test.go` |
| FR-DIAG | `cmd/smirror/main.go` (commands) | `preflight_test.go` |
| FR-SVC | `internal/service/` | Manual (Windows SCM integration) |
| FR-CLI | `cmd/smirror/main.go` | `preflight_test.go` |
| FR-ANOM | `internal/anomaly/` (planned) | `anomaly_test.go` (planned) |
| NFR-* (25010) | Cross-cutting | Various `*_test.go` |

#### 10.4.3 Bug Hunt Traceability

Bug hunt markers (SM-031 through SM-061) provide a secondary traceability chain from discovered defects to regression tests to the requirements they exercise:

| Marker | Defect Summary | Requirements Exercised |
|--------|---------------|----------------------|
| SM-036/037 | Filter rule matching semantics (first-match vs last-match) | FR-FILTER-04, NFR-FC-02 |
| SM-041 | Symlink-to-directory escape | FR-WATCH-05, NFR-AC-02 |
| SM-044 | Filter hot-reload race condition | FR-FILTER-06, FR-FILTER-07 |
| SM-047 | SQLite concurrent writer deadlock | FR-STATE-03, NFR-RC-01 |
| SM-050 | Burst-delete event drops | FR-WATCH-07, NFR-FT-04 |
| SM-053 | Verify double-counts LEAKs | FR-GHOST-02, NFR-FC-03 |
| SM-054 | Ghost scan race condition | FR-GHOST-06, NFR-FT-02 |
| SM-055 | Auto-verify missing LEAK distinction | FR-GHOST-02, NFR-FC-03 |
| SM-059/060 | Circuit breaker for per-mirror backoff | FR-QUEUE-05, NFR-FT-03 |

### 10.5 Resolved Questions

| # | Question | Resolution | Date |
|---|----------|-----------|------|
| 1 | Should bidirectional sync be a goal? | **No.** Out of scope permanently. Composable from two instances. | 2026-04-01 |
| 2 | Is a GUI worth the complexity for v1.0? | **No.** Post-v1.0. Mature CLI first, then GUI for stable feature surface. | 2026-04-01 |
| 3 | Should per-mirror delete policy be prioritized? | **Yes.** Committed for v0.5.0. | 2026-04-01 |
| 4 | Is sync log pruning needed before v1.0? | **Yes.** Committed for v0.5.0. Unbounded growth is a production risk. | 2026-04-01 |
| 5 | Should Linux/macOS service mode be pursued? | **Post-v1.0.** Extended to "any OS with rclone support." | 2026-04-01 |
| 6 | Multi-destination sync? | **Post-v1.0.** Strong use case, high complexity. | 2026-04-01 |
| 7 | Pre/post-sync hooks: core or external? | **Core.** Rich hook infrastructure committed for v0.6.0. | 2026-04-01 |
| 8 | Performance SLAs for v1.0? | **Defined.** See Section 4.2 (tightened targets). | 2026-04-01 |

### 10.6 Open Questions

1. Should quiescence duration be per-mirror configurable? (Code projects: 200ms; Office-heavy dirs: 2-5s)
2. Should adaptive cooldown parameters (base, max, window) be user-configurable or hardcoded?
3. What is the right anomaly report retention policy — time-based (30 days) or size-based (100 MB) or both?
4. Should `FR-WATCH-11` (watch depth) be a per-mirror config or a filter pattern approach (e.g., `/*` in `.syncignore`)?
5. Should anomaly outbound reporting require explicit opt-in, or default to enabled when internet is available?

---

## 11. Implementation Plan

### 11.1 Release Roadmap

```
v0.4.0               v0.5.0              v0.6.0              v0.7.0              v1.0
─────────────────── ─────────────────── ─────────────────── ─────────────────── ───────
Ghost cleanup        Per-mirror config   Anomaly reporter    Hook system         SLA validation
FairQueue            Adaptive sync       Anomaly patterns    USN journal         Conformance
Circuit breaker      DB hardening        Self-diagnostics    Platform hardening  Release polish
                     Filter safety
```

### 11.2 Pre-Release SLA Gate

Every release must pass `test/sla_smoke.ps1` (smoke SLA) before tagging. This script validates:
- **Sync latency** (NFR-TB-02): p95 < 5s per file
- **Detection latency** (NFR-TB-01): file write → remote arrival < 3s
- **Zero data loss**: 50-file sync with checksum verification + `smirror verify`
- **Burst throughput** (NFR-TB-06): 50 rapid files all arrive correctly
- **Memory sanity** (NFR-RU-01): idle RSS < 50MB

Full load SLA testing (32 mirrors, 100K files) begins at v1.0.

### 11.3 v0.4.0 — Foundation Release (Current, Near-Complete)

**Theme**: Core sync engine with production-grade scheduling.

Already implemented:
- Ghost cleanup (LEAKs + ORPHANs) with dry-run preview
- FairQueue (dedup, move-to-back fairness, priority deletes)
- Circuit breaker with per-mirror exponential backoff
- Per-file cooldown (fixed 30s — to be replaced in v0.5.0)
- 530+ unit tests + 2 fuzz tests, 6 integration test scripts

Remaining for 0.4.0 release:
| Work Item | Requirements | Effort | Risk |
|-----------|-------------|--------|------|
| FR-SYNC-14: Per-mirror rclone extra flags | FR-SYNC-14 | Small | Low — config parsing + flag injection |
| Stabilization and release testing | — | Medium | — |

### 11.4 v0.5.0 — Adaptive Sync & Data Hardening

**Theme**: Signal-based intelligence, per-mirror configurability, production data safety.

| Work Item | Requirements | Effort | Risk | Dependencies |
|-----------|-------------|--------|------|-------------|
| **Signal-based adaptive cooldown** | FR-SYNC-13 | Medium | Medium — algorithm tuning needed | FairQueue event history tracking |
| **Adaptive reconciliation** | FR-SYNC-09 | Medium | Low — extend existing timer logic | None |
| **Per-mirror delete policy** | FR-ASP-06 | Small | Low — config + routing change | None |
| **Per-mirror rclone extra flags** | FR-SYNC-14 | Small | Low | Already in config struct |
| **Sync retry on transient failure** | FR-SYNC-16 | Small | Low — single retry before circuit breaker | None |
| **State DB auto-migration framework** | FR-ASP-16, FR-STATE-06 | Medium | Medium — must be forward-compatible | None |
| **Sync log pruning** | FR-ASP-11, FR-STATE-04 | Small | Low — retention policy + cleanup query | State DB migration framework |
| **Quarantine auto-purge** | FR-DEL-09 | Small | Low — rclone lsjson + age check during reconciliation | None |
| **Atomic directory delete (rclone purge)** | FR-DEL-07 | Small | Low — replace per-file loop with rclone purge | None |
| **Filter syntax error safety** | FR-FILTER-11 | Small | Low — catch parse error, keep last-known-good generation | None |
| **Documented exit codes** | FR-CLI-07 | Small | Low — define constants, replace os.Exit(1) calls | None |
| **Queue: remove artificial limit** | FR-QUEUE-08 | Small | Low — remove maxSize, add memory monitoring | None |
| **Queue overflow → reconciliation trigger** | FR-QUEUE-10 | Small | Low — threshold check in Enqueue | FR-QUEUE-08 |
| **Performance measurement baseline** | NFR-TB-*, NFR-RU-* | Medium | Low — instrumentation + benchmark harness | None |

**Estimated scope**: ~15 work items, mix of small and medium. This is the "make it production-ready" release.

### 11.5 v0.6.0 — Anomaly Intelligence

**Theme**: Self-diagnosing system, automated failure analysis. Moved ahead of platform hardening
because anomaly data is most valuable while the system is still maturing — it catches unknown
unknowns in the field and informs which hardening investments (hooks, USN, Event Log) matter most.

| Work Item | Requirements | Effort | Risk | Dependencies |
|-----------|-------------|--------|------|-------------|
| **Anomaly classification engine** | FR-ANOM-01, FR-ANOM-02 | Medium | Medium — taxonomy design | None |
| **Anomaly recording with context snapshots** | FR-ANOM-03, FR-ANOM-04 | Medium | Low — structured JSON writer | FR-ANOM-01 |
| **Causal hypothesis chains** | FR-ANOM-05 | Large | High — requires deep domain knowledge encoding | FR-ANOM-03 |
| **Anomaly pattern detection** | FR-ANOM-06 | Large | High — statistical analysis, trend detection | FR-ANOM-03 |
| **Status integration** | FR-ANOM-07 | Small | Low — extend existing status output | FR-ANOM-03 |
| **Report sanitization** | FR-ANOM-08 | Small | Low — path redaction, credential scrubbing | FR-ANOM-04 |
| **Configurable outbound reporting** | FR-ANOM-11 | Medium | Medium — endpoint design, opt-in/out logic, zero-traffic guarantee | FR-ANOM-04 |
| **Anomaly notification (toast + future webhook)** | FR-ANOM-09 | Small | Low — extend existing notify package | FR-ANOM-03 |
| **Report auto-rotation** | FR-ANOM-10 | Small | Low — age/size-based cleanup | FR-ANOM-04 |

### 11.6 v0.7.0 — Hooks & Platform Hardening

**Theme**: Extensibility infrastructure, cross-platform readiness. Informed by anomaly data
collected during v0.6.0 field usage — hardening priorities guided by real failure patterns.

| Work Item | Requirements | Effort | Risk | Dependencies |
|-----------|-------------|--------|------|-------------|
| **Pre/post-sync hook system** | FR-ASP-17 | Large | Medium — design callback points, error handling, timeout, security | Sync engine callback architecture |
| **USN journal integration** | FR-WATCH-10 | Large | High — Windows-specific, kernel API, circular buffer handling | None |
| **Gitignore conformance test suite** | FR-FILTER-01 | Medium | Low — test-only, no production code change | None |
| **Windows Event Log integration** | FR-SVC-08 | Small | Low — golang.org/x/sys/windows has eventlog package | None |
| **Symlink documentation & info logging** | FR-WATCH-06 | Small | Low — doc + log level change | None |
| **Watch depth configurability** | FR-WATCH-11 | Small | Low — filter at watcher level | None |

### 11.7 v1.0 — Release Readiness

**Theme**: SLA validation, conformance, release polish.

| Work Item | Requirements | Effort | Risk |
|-----------|-------------|--------|------|
| **Performance SLA validation** | All NFR-TB-*, NFR-RU-*, NFR-CA-* | Large | Medium — need benchmark infrastructure |
| **Comprehensive report-bug sanitization** | NFR-CO-03 | Medium | Low |
| **32-mirror load test** | NFR-CA-01 | Medium | Medium — resource contention at scale |
| **100K-file stress test** | NFR-CA-02 | Medium | Medium — state DB performance under load |
| **Documentation audit** | — | Medium | Low |
| **Security audit** | NFR-SEC-* | Medium | Medium — self-audit (`docs/security-audit-2026-04-18.md`) + adversarial multi-role panel reviews are the project's standing process |
| **Release packaging** | — | Small | Low |

### 11.8 Implementation Priorities (Decision Framework)

When choosing what to implement next within a release, apply this priority order:

1. **Correctness** — Fix bugs, close data-loss paths (FR-FILTER-11, FR-DEL-07 atomic, FR-QUEUE-08)
2. **Reliability** — Production hardening (FR-STATE-06 auto-migration, FR-SYNC-16 retry, FR-DEL-09 auto-purge)
3. **Intelligence** — Signal-based behavior (FR-SYNC-13 adaptive cooldown, FR-SYNC-09 adaptive reconciliation)
4. **Observability** — Anomaly detection, diagnostics (FR-ANOM-*, FR-SVC-08, FR-CLI-07)
5. **Extensibility** — Hooks, per-mirror config (FR-ASP-17, FR-ASP-06)
6. **Performance** — SLA measurement and optimization (NFR-TB-*, NFR-RU-*, NFR-CA-*)

### 11.9 Risk Register

| Risk | Impact | Probability | Mitigation |
|------|--------|------------|------------|
| USN journal API complexity exceeds estimate | v0.6.0 delay | Medium | Time-box to 2 weeks; fall back to reconciliation-only if blocked |
| Adaptive cooldown algorithm needs extensive tuning | Sync latency regression | Medium | A/B test against fixed cooldown; keep fixed as fallback |
| Hook system security (arbitrary shell execution) | Command injection, privilege escalation | Low | Sandboxing, timeout enforcement, no credential inheritance |
| Performance SLAs not met at 32 mirrors / 100K files | v1.0 scope reduction | Low | Profile early (v0.5.0 baseline); optimize incrementally |
| Anomaly hypothesis chains produce false positives | User trust erosion | Medium | Start with high-confidence hypotheses only; mark confidence level |
| rclone breaking changes in future versions | Sync failures after rclone upgrade | Low | Pin minimum version; test-mirrors catches post-upgrade issues |
