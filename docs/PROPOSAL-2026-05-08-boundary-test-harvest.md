# PROPOSAL — Inter-component-handoff boundary-test harvest (post-SM-216)

**Status**: brainstorm; awaiting maintainer prioritization.
**Author**: Raveh + Claude, 2026-05-08 (after SM-216 post-mortem).
**Companion to**: `system-validation/installer_handoff_seam_test.go`
(the structural ratchet for SM-216 specifically) and the V&V/Release
panel post-mortem documented in commit `592c805`.

---

## The class we were missing

**Inter-component handoff boundary tests** — tests that exercise the
SEAM where component A produces state X for component B. Each handoff
is a contract; the contract's inputs include "what happens when X is
empty, null, missing, corrupt, unexpected, or stale?"

SM-216 (silent-telemetry-failure on v1.0.0 MSI consent) was a textbook
member of this class. The MSI produced state X = `(TelemetryTier set,
install_id NOT set)` and the daemon's `SendInstallEventsIfDue`
expected X = `(TelemetryTier set, install_id set)`. Neither component
was wrong individually; the contract between them was never checked.

Each component in SelectiveMirror was unit-tested. The seams were
not. SM-216 escaped six layers of test coverage to land in v1.0.0
because every layer covered ONE component in isolation.

The fix pattern we should ratchet in: **for every handoff, enumerate
the boundary conditions and write a test per condition.**

---

## Components with handoffs

SelectiveMirror has roughly fifteen inter-component handoffs. Each is
a candidate seam:

| # | Producer | Consumer | Crossing |
|---|---|---|---|
| 1 | MSI installer | smirror runtime | registry → state DB → install_events |
| 2 | smirror runtime | state DB | Go ↔ SQLite (meta keys, schema migrations) |
| 3 | smirror runtime | Cloudflare Worker | HTTP POST + HMAC |
| 4 | Cloudflare Worker | Supabase PostgREST | HTTP POST + RPC routing |
| 5 | Supabase PostgREST | Postgres function | RPC binding → SECURITY DEFINER |
| 6 | Postgres function | Vault | `vault.decrypted_secrets` read |
| 7 | smirror runtime | rclone | subprocess invocation, stdout / stderr / exit-code parse |
| 8 | smirror runtime | fsnotify / ReadDirectoryChangesW | Windows kernel API |
| 9 | smirror runtime | Windows SCM | service install / start / stop / status |
| 10 | smirror runtime | Scheduled Task | schtasks.exe + XML |
| 11 | smirror runtime | Windows Notify | toast notifications |
| 12 | CLI commands | smirror runtime | config + state DB read/write |
| 13 | selfupdate | GitHub Releases API | HTTP GET + JSON |
| 14 | report-bug | GitHub Issues web form | URL prefilling |
| 15 | state DB schema migrations | binary version | one schema version ↔ next |

The SM-216 ratchet test (commit `592c805`) is for handoff **#1** only.
Handoffs **#2 – #15** are still without dedicated boundary tests for
their failure modes.

---

## Candidates the harvest could surface

**Each candidate is a one-line specification of the test that
*should* exist.** Each test is small (10–50 LOC); the value is in
the *cumulative* coverage as the gate fills out.

### Highest-yield (P1 — likely to surface real bugs at v1.0.x)

1. **MSI TelemetryTier=garbage** — manual registry edit produces
   `TelemetryTier="hello"`. Daemon must `ReadTier` → fail-closed to
   None. Currently uncovered as a behavioral test; the `IsValid()`
   guard in `tier.go` is unit-tested but not against the real registry-
   passthrough path.

2. **MSI TelemetryTier="" (empty string)** — distinct from "key
   missing." Currently `ReadTier` falls through to default (None),
   but no test pins that behavior. A future refactor of state-DB-
   first-then-registry priority could regress this silently.

3. **State DB schema migration: binary newer than DB** — operator
   runs v1.0.5 against a state.db created by v1.0.0. Migrations
   must apply forward cleanly. Untested across a real version step
   (the existing migration tests use the same binary's schema).

4. **State DB schema migration: binary older than DB** — operator
   downgrades from v1.0.5 to v1.0.0 (rollback after a bad release).
   Binary should refuse-to-run with a clear error, not silently
   corrupt the DB. Currently relies on `last_recorded_version` check;
   no test asserts the refuse-with-clear-error contract.

5. **rclone returns text/html on stderr** — happens in some corporate
   network configurations where rclone hits a proxy login page.
   `rclone.Detect` parses stdout; what happens to stderr garbage?

6. **Worker returns 5xx text/html** — Cloudflare-edge throttling
   (round-9 finding). The `internal/telemetry/contribute.go` Worker-
   rewrite-to-502 is structurally tested; behavioral test under
   simulated upstream 5xx + HTML body is missing.

7. **Vault secret rotated mid-call** — `verify_versioned_hmac` reads
   `vault.decrypted_secrets`. If the secret rotates between contract
   checks, the function-side derived key would mismatch. Today this
   is "operator awareness" only; no test gates against it.

### Good-hygiene (P2 — known surface, lower-frequency real bugs)

8. **MSI uninstall leaves TelemetryTier in registry** — if a user
   uninstalls + re-installs, do they see the consent dialog or skip
   it (the v1.0.1 preserve-tier flow)? Behavioral.

9. **MSI HKLM vs HKCU tier disagreement** — power user manually edits
   HKCU after MSI sets HKLM. Which wins? The runtime reads HKLM
   only; this is the right behavior but no test pins it.

10. **CLI `smirror addmirror` on a path that's already a mirror** —
    config-edit-locked (covered) but the boundary case "the path
    isn't bit-identical, just symlinks/Junction-points to an
    existing mirror's path" is untested.

11. **Scheduled Task runs when user is not logged on** — per-user
    task install behavior under that condition. Documented as a
    known gap; not tested.

12. **selfupdate: GitHub release missing the MSI asset** — `gh release
    download --pattern "*.msi"` returns nothing. selfupdate should
    fail with a clear error pointing at the manual-download URL.
    Current `cmdSelfUpdate` handles this but the error path isn't
    tested.

### Lower-yield-but-interesting (P3 — surface area broad, real-bug
likelihood lower)

13. **fsnotify buffer overflow under load** — Windows
    ReadDirectoryChangesW has a finite buffer. Under high write
    rate, events are dropped silently. The watcher should fall
    back to a full scan. The fallback exists (per memory); no test
    asserts it activates.

14. **rclone subprocess hang** — rclone enters an infinite retry
    loop on a flaky backend. smirror's subprocess.kill timeout
    (does it have one?) should fire.

15. **State DB locked by another process** — a power user runs
    `sqlite3 state.db` interactively with a transaction held open;
    smirror's open path should detect and fail-fast (not block
    forever).

---

## Recommended harvest schedule

**Pre-tag (v1.0.1 release readiness)**:
- Land #1 (TelemetryTier=garbage) and #2 (TelemetryTier="") together
  — both touch the same surface, ~30 LOC + 2 tests, would catch a
  silent-fail-closed regression that's plausible.

**v1.0.x maintenance window**:
- #3 + #4 (schema migration boundaries) — the moment any v1.0.x
  patch touches the schema, this is highest-priority. Otherwise
  defer.
- #6 (Worker 5xx HTML upstream) — already structurally checked; the
  behavioral test would harden the `contribute.go` rewrite branch.
  Worth 1 commit cycle.
- #8 (MSI uninstall + reinstall preserve-tier) — extends the v1.0.1
  binary-consent change's contract.

**v1.1 / opportunistic**:
- The remaining seven. Each is small; collectively they tighten the
  gate from "no SM-216-class bug ever shipped again at the MSI
  seam" to "no SM-216-class bug at any seam."

---

## Effort estimation per candidate

Most candidates are small. Rough budget:

| Test type | LOC | Effort |
|---|---|---|
| Source-property (regex over WiX/Go/SQL) | ~20–40 LOC | ½ hour |
| Behavioral integration (in-process mock) | ~50–100 LOC | 1–2 hours |
| Behavioral end-to-end (real MSI install + daemon run) | ~150–300 LOC + Windows runner | 4–8 hours |

The 15-test full harvest is roughly 50 hours of effort (one focused
week). The pre-tag subset (#1, #2) is under one hour.

---

## Process improvement (not just tests)

The harvest also surfaces a process gap:

  - **Pre-tag boundary-test pass.** Before a release tag, walk the
    handoff list above and ask "for each handoff, what's the input
    boundary I'm trusting?" If a boundary isn't in the test matrix,
    add it before tagging. SM-216 would have been caught this way
    even without a behavioral test — the question "what if MSI sets
    tier but not install_id?" was simply never asked.

  - **Component-pair documentation.** The 15 handoffs deserve a
    doc — `docs/inter-component-contracts.md` — listing each pair
    and the contract terms. This becomes the input to the boundary-
    test pass. Currently the contracts are implicit.

  - **System-validation gate ratchet** —
    `system-validation/installer_handoff_seam_test.go` is the
    template. Each handoff harvested produces a similar gate file
    (`system-validation/<handoff>_seam_test.go`). Once the file
    exists, removing the production code or the regression test is
    a CI-fail; the ratchet stays in place across maintainer churn.

---

## Decision asks

1. **Approve the test class as a first-class concept**
   ("inter-component handoff boundary tests") and add it to the
   release runbook's pre-tag checklist? Yes / No / Modify.

2. **Approve the pre-tag subset (#1, #2)** for v1.0.1? Yes / No.

3. **Approve the v1.0.x subset (#3, #4, #6, #8)** as opportunistic
   harvesting during the patch cycle? Yes / No / Modify.

4. **Component-pair contract documentation**
   (`docs/inter-component-contracts.md`) — write it as a one-time
   pass? Yes / Defer to v1.1.

5. **Fold this memo into the release runbook?** The "process
   improvement" section above is reusable; the candidate list is
   tactical. Could split.

---

## Files of record

This memo: `docs/PROPOSAL-2026-05-08-boundary-test-harvest.md`.

Cross-references:
  - `system-validation/installer_handoff_seam_test.go` — the
    template the harvest extends.
  - Commit `592c805` — V&V/Release post-mortem of SM-216.
  - Commit `8e82d40` — DEFECT-1 / SM-216 fix in
    `internal/telemetry/install_events.go`.
  - `docs/PROPOSAL-2026-05-03-msi-binary-consent.md` — the
    parallel UX proposal (different bug, same crime scene).

— v1.0.1+ design, awaiting maintainer prioritization.
