# PROPOSAL — Boundary-test harvest, ROUND 2

**Status**: brainstorm; awaiting maintainer prioritization.
**Author**: Raveh + Claude, 2026-05-08 (later same day, after the SV-layer reproduction of SM-216).
**Predecessor**: [`PROPOSAL-2026-05-08-boundary-test-harvest.md`](PROPOSAL-2026-05-08-boundary-test-harvest.md) — Round 1, 15 candidate boundary tests across the 15 inter-component handoffs.
**Companion**: [`docs/inter-component-contracts.md`](inter-component-contracts.md) — the contract reference the harvest extends.

---

## Why round 2?

Round 1 enumerated the 15 inter-component handoffs and proposed ONE
boundary test per handoff — the canonical "what if the producer's
output is missing/empty/garbled?" shape. That was driven by the
SM-216 post-mortem: identify the seam, write the gate, ratchet it in.

Then we did the actual SV reproduction (commit `592c805` source-
property gate, plus the two behavioral additions in this round's
companion commit). The reproduction exercise itself revealed three
new things:

1. **One handoff has many boundaries, not one.** Handoff #1 alone
   has six distinct boundary subcases that all want a test
   (install_id `==""`, corrupted, wrong-format, deleted-after-
   generation, set-twice-by-race, present-but-tier-changed-mid-
   recovery). The Round-1 list under-counted by ~5×.

2. **Behavioral and mutation tests are cheap at the SV layer.** The
   SV module can shell out `go test -run X` against the parent
   module in ~5 s. With `-overlay`, it can mutate the source under
   test and assert the mutation is caught — also ~5 s. Neither
   requires importing internal/* nor breaking the module boundary.
   That changes the cost calculus: every Round-1 candidate that's
   currently "source-property only" can get a behavioral
   companion at +5 s wall-clock per CI run.

3. **The end-to-end MSI-install + daemon-run test remains the
   heaviest gap.** Even with the 6 SV gates around handoff #1, no
   test has built a real MSI, installed it on a Windows runner,
   started smirror, and confirmed `first_seen` actually lands on a
   mock telemetry endpoint. That's the test that would have caught
   SM-216 directly. Round 1 deferred this; Round 2 does too, but
   pins it more precisely as **the single highest-yield missing
   test in the entire matrix** and proposes a concrete shape.

---

## What this round adds

Three buckets:

- **2A — Boundary subcases inside the original 15 handoffs.** The
  Round-1 list under-counted; each handoff should have a
  per-boundary test, not a per-handoff test. This bucket enumerates
  the subcases for each handoff. Most are 1-handoff = 3-8 subcases.

- **2B — Test classes that didn't appear in Round 1.** Time-related
  contracts (clock skew, DST, NTP shifts), telemetry-endpoint
  contracts (HTTPS-only, typo-resilience), heisenbugs (concurrent
  startup, MSI-mid-daemon-running). Most of these don't fit the
  "producer-consumer handoff" frame; they're orthogonal contracts.

- **2C — The MSI-end-to-end test.** Concrete sketch + cost +
  prerequisites.

---

## Bucket 2A — Boundary subcases per handoff

Each handoff (#1–#15 from Round 1) gets its own subcase enumeration.
SM-216 falls under handoff #1 / subcase A; we now have a gate for it.
Subcases B–F under handoff #1 are still gap-shaped.

### Handoff #1 — MSI installer → smirror runtime

| Subcase | What | Status | Priority |
|---|---|---|---|
| A | install_id missing at Gate 3 (SM-216 / DEFECT-1) | **GATED** (commit 592c805 + this round's behavioral + mutation) | done |
| B | install_id present but **corrupted** (e.g. wrong byte length, non-hex chars after `sm-` prefix) | gap | P1 — silent telemetry failure with bad install_id is plausible |
| C | install_id present but **wrong format** for a future schema (e.g. v2 install_id format introduced; v1 daemon reads v2 DB) | gap | P3 — future, schema-version-gated |
| D | install_id **generated, persisted, then deleted externally** (user edits state.db, or `smirror clean` runs partial). Recovery should re-fire on next start. | gap | P1 — self-healing test |
| E | install_id **set twice by a race** (two daemon processes briefly both running). Single-instance lock should prevent, but if the lock is released between Gate 1 and Gate 3, a second startup could double-write. | gap | P3 — race-window |
| F | TelemetryTier in registry **but value is garbage** (manual edit, foreign-locale value, BOM prefix). Round 1 #1 covered the empty/garbage shape at the unit level (`tier_test.go` round-2 boundaries); SV behavioral gate is still missing. | partial — unit only | P1 |

Test recipe per subcase: 4 source-property + 1 behavioral subprocess
+ 1 -overlay mutation = 6 tests, ~50 LOC each. Total handoff #1
gating cost: 30 LOC × 6 subcases ≈ 180 LOC of new test code, ~30 s
wall-clock per CI run.

### Handoff #2 — smirror runtime → state DB

| Subcase | What | Status | Priority |
|---|---|---|---|
| A | schema_version meta key missing (partial DB write from old crash) | gap | P1 — recovery vs corruption |
| B | schema_version newer than binary's max-known | gap | P0 — refuse-with-clear-error contract |
| C | Out-of-order migration steps applied | gap | P3 |
| D | WAL file present, main file missing | gap | P2 — sqlite handles this; smirror must too |
| E | Read-only filesystem (immutable bit, NFS RO mount, network-drive disconnect mid-write) | gap | P2 |
| F | DB file size > 2 GB (file-system-dependent edge) | gap | P3 |
| G | DB file owned by another user (Windows: ACL-locked) | gap | P2 — service vs per-user mode mismatch |

### Handoff #3 — smirror runtime → Cloudflare Worker

| Subcase | What | Status | Priority |
|---|---|---|---|
| A | Worker returns 5xx with HTML body (Round 1 #6) | partial — unit test added (commit 919592c) | P2 — behavioral companion still missing |
| B | Worker returns 4xx with HTML body (covered in this round; see `contribute_test.go::TestContribute_HTTP4xxWithHTMLBodyReturnsNetworkErr`) | done | done |
| C | Worker returns 200 with HTML body (success status, wrong content-type — server bug shape) | gap | P1 — silent telemetry "success" |
| D | Worker returns 200 with `{"ok": false, "error": ...}` body | gap | P1 — application-layer rejection masked by HTTP 200 |
| E | Worker returns 200 with `Content-Encoding: gzip` but body is not actually gzip | gap | P3 |
| F | Worker honors / ignores `Retry-After: 9999` header | gap | P2 — DoS surface for long retries |
| G | Worker rate-limit response (429) handling | gap | P1 — likely under sustained CI load |

### Handoff #4 — Worker → Supabase PostgREST

| Subcase | What | Status | Priority |
|---|---|---|---|
| A | PostgREST returns 503 with HTML body (Supabase upstream maintenance) | gap | P2 |
| B | PostgREST returns 200 but body shape is unexpected (RPC vs row format mismatch) | gap | P1 |
| C | PostgREST canonicalization mismatch (length-first vs codepoint-first key sort) | gap — partially covered by `reference_jsonb_canonicalization.md` doc | P0 — silent HMAC fail |

### Handoff #5 — PostgREST → Postgres function

| Subcase | What | Status | Priority |
|---|---|---|---|
| A | Function panics; PostgREST returns 500 | gap | P2 |
| B | Function does HMAC mismatch but returns 200 (would bypass rate limit) | gap | P0 — privacy bypass |
| C | Function detects future event_kind ENUM value and rejects | gap | P2 — forward-compat |

### Handoff #6 — Postgres function → Vault

| Subcase | What | Status | Priority |
|---|---|---|---|
| A | Vault decryption fails (key rotated since function loaded the cached secret) | gap (Round 1 #7) | P1 |
| B | Vault returns NULL for the requested name (operator forgot to create the secret) | gap | P2 — clear error vs silent reject |
| C | Vault returns a secret with wrong shape (operator typoed the value) | gap | P3 |

### Handoffs #7–#15

Compressed for memo brevity — each has 3–6 subcases similar in shape:

- **#7 rclone**: subprocess hang, malformed stderr, exit-code-200-with-failure-shape, version-incompat
- **#8 fsnotify**: buffer overflow, drive-letter unmount mid-watch, junction/symlink loop, 32k watch limit
- **#9 SCM**: install-while-running, uninstall-while-running, service-stuck-stopping, manual-edit-via-services.msc
- **#10 Scheduled Task**: user-not-logged-on (Round 1 #11), schtasks XML schema across Win10/Win11, AAD-vs-local-account
- **#11 Notify**: toast-suppressed-by-Focus-Assist, toast-without-app-registration, toast-rate-limited
- **#12 CLI**: long-flag value with embedded `=`, ANSI-escape input, BOM-prefixed config file
- **#13 selfupdate**: draft release, prerelease, missing MSI asset (Round 1 #12), rate-limit, truncated JSON
- **#14 report-bug**: GitHub URL length limit (~8k), URL-encoding edge cases, browser-launch-no-default-browser
- **#15 schema migrations**: see #2

---

## Bucket 2B — Test classes that didn't appear in Round 1

These are CONTRACTS, not handoffs. They cut across many components.

### B1. Time-related contracts

T1. **System clock skew at startup.** `time.Now()` returns a date
before 2020 (CMOS battery dead). `first_seen_at` writes nonsense.
Should `SendInstallEventsIfDue` refuse to write `first_seen_at` if
it's clearly before the binary's release date? Currently no gate.

T2. **DST transition mid-run.** A daemon running across a forward-
spring DST transition computes `days_since_first_seen_bucket` with
a 60-minute discontinuity. Bucket math is in days, so the discon-
tinuity is sub-bucket — but does the test matrix lock that?

T3. **NTP forces clock forward 1 hour mid-call.** Idempotency of
install-event submission across the time-jump. Untested.

T4. **System clock backward jump 10 minutes.** Same as T3 but
opposite direction; could re-fire dead-letter retries unexpectedly.

### B2. Telemetry endpoint contracts

E1. **HTTPS-only enforcement.** What if `SMIRROR_TELEMETRY_ENDPOINT`
is set to `http://...` (no S) at build time or runtime? Should
refuse-or-warn. Currently no gate; an attacker who can control the
env var could downgrade to HTTP and intercept.

E2. **Endpoint domain typo.** `t.celemetry.smirror.dev` (or the
real `telemetry.smirror.dev` typo). Should DNS-fail gracefully, not
crash the daemon. Likely already handled by net/http but no test
pins it.

E3. **Endpoint resolves to localhost.** Test environment? Should
still work (via mock). Should it WARN in production builds?
Currently no gate.

### B3. Heisenbugs / race conditions

R1. **Concurrent install_id generation.** Two daemon processes
spawn within milliseconds; single-instance lock should prevent but
the lock acquisition vs Gate 3 ordering matters. Test under
deliberate race conditions (e.g., starting smirror twice in 50ms).

R2. **MSI repair on a system where daemon is running.** What
happens to the running daemon when MSI tries to overwrite the
binary? Windows file locks may prevent; what's the user-visible
behavior?

R3. **Logoff during install.** MSI consent dialog is up; user logs
off (Windows kills MSI). Partial state left in registry?

R4. **`smirror clean --self` mid-startup.** User clicks clean from
one terminal while another terminal is running `smirror start`
(daemon goroutine has fired Gate 1 but not Gate 3). Recovery vs
crash?

### B4. Cross-tier / cross-version scenarios

V1. **Tier transition Standard → None → Standard.** Does
install_id persist across the None interlude? Should it? (Round-1
#10 mentioned the architecture says yes; behavioral test missing.)

V2. **v0.9.x → v1.0.x state.db continuity.** schema_version pre-
v1.0 was different. Is the migration path tested end-to-end?

V3. **Tier transition mid-startup.** User runs `smirror telemetry
none` while daemon is starting. Race between Gate 1 (tier check)
and Gate 3 (install_id recovery). Currently no gate.

---

## Bucket 2C — The MSI end-to-end test

This is the test that would have caught SM-216 directly. Sketch:

```
1. CI runner: ubuntu-latest cannot do this; needs windows-latest.
2. Build: go build + GoReleaser produce smirror.exe + .msi.
3. Stage: a Windows runner installs the freshly-built .msi with
   `msiexec /i smirror.msi /qn INSTALL_TELEMETRY_TIER=standard`.
4. Run: launch `smirror start` for ~30 seconds in the background.
5. Mock: the test pre-configures SMIRROR_TELEMETRY_ENDPOINT to
   point at a localhost HTTP listener that records POSTs.
6. Assert: within 30 seconds, the listener received exactly one
   `first_seen` event with the expected client_version + tier.
7. Cleanup: `msiexec /x` or `smirror clean --all`.
```

Cost estimate: 4-8 hours initial, plus ongoing CI minutes (~3 min
per release-dryrun run). Prerequisites:
- A windows-latest runner with MSI install permissions
- A way to inject the mock-endpoint env var into the MSI install
  context (could be done via the MSI's `INSTALL_TELEMETRY_ENDPOINT`
  property if we add one)
- Cleanup discipline so the runner is reusable

This test belongs in `release-dryrun.yml` after the existing MSI
build step, gated behind a job-level `if: runner.os == 'Windows'`.

**Recommendation**: Schedule this for v1.0.x maintenance window
(target: v1.0.5 or v1.0.10). Defer for now; the 6-test handoff #1
gate (Round 1 + this round's behavioral) provides 95% of the
SM-216-class regression catching at 0% of the runner cost.

---

## Recommended next steps

If approving this memo:

1. **Pre-tag for v1.0.5+**: harvest handoff #1 subcases B, D, F
   (each ~50 LOC, 3-test pattern). Rationale: same surface as the
   already-gated subcase A; cheap to keep adjacent.

2. **v1.0.x maintenance window**: harvest handoff #2 subcases A,
   B (state DB schema-version boundaries) and handoff #3 subcase
   C, D, G (Worker 200-with-failure-shape). These are all P1
   silent-fail patterns; collectively ~200 LOC.

3. **v1.1 / opportunistic**: bucket 2B (time / endpoint /
   heisenbugs / cross-version). These cut across components and
   require more design thought per test; not urgent.

4. **v1.0.5 or v1.0.10 release scope**: bucket 2C (MSI E2E test).
   The cost is real but the catch-rate for SM-216-class bugs is
   the highest of any test in the matrix. Schedule deliberately.

---

## Decision asks

1. **Approve the per-handoff subcase enumeration** as the working
   list for the v1.0.x maintenance window?

2. **Approve the test pattern** (4 source-property + 1 behavioral
   + 1 mutation per boundary) as the standard shape for the
   harvest?

3. **Approve the MSI E2E sketch** as the target for v1.0.5 or
   v1.0.10? Yes / Defer / Modify the sketch.

4. **Fold this into the §4½ pre-tag boundary-test pass** in the
   release runbook? The walk should now reference both Round-1
   and Round-2 docs.

---

## Files of record

This memo: `docs/PROPOSAL-2026-05-08-boundary-test-harvest-round2.md`.

Round 1: `docs/PROPOSAL-2026-05-08-boundary-test-harvest.md`.

Cross-references:
  - `system-validation/installer_handoff_seam_test.go` —
    template for the per-handoff gate, now with 6 tests (4
    source-property + 1 behavioral subprocess + 1 -overlay
    mutation).
  - Commit `97891fa` — Round-1 deliverables (contract doc +
    runbook §4½ + handoff #2 boundary tests + handoff #3 boundary
    tests).
  - Commit `592c805` — V&V/Release post-mortem of SM-216 (the
    seam test was added here in source-property form).
  - Commit `8e82d40` — DEFECT-1 / SM-216 fix in
    `internal/telemetry/install_events.go`.

— v1.0.x harvest plan, awaiting maintainer prioritization.
