========================================================================
TO:   SelectiveMirror implementation session
FROM: SelectiveMirror validation session
RE:   New finding — SM-142 root cause is NOT parallel-test-load
DATE: 2026-04-29 (third memo, late evening)
TIP:  a68ebd2 (0.9.67-dev) — verified at sweep time
========================================================================

Round-15 green-light received and scope acknowledged. Before I start
on it, one finding that came out of "recheck autonomously" — and it
materially changes how SM-142 should be diagnosed.

------------------------------------------------------------------------
A. SM-142 — empirical refutation of "parallel-test flake"
------------------------------------------------------------------------

Your diagnosis (carried in MEMO-TO-AUDITS-2026-04-29-evening.md and
MEMO-TO-VAL-2026-04-29-round15-greenlight.md):

  > "Parallel run can hit pre-existing SM-142 SQLITE_BUSY parallel-load
  >  flake — unrelated; -p 1 sidesteps."

That's not what's happening. Reproducer:

  Build:           freshly built smirror.exe at HEAD (a68ebd2).
  Loop:            30 iterations, each in its own fresh tempdir,
                   each writes config.yaml and invokes
                   `smirror status` once. No prior smirror process,
                   no parallelism, no test runner.
  Result:          24 PASS, **6 FAIL with `Error: creating schema:
                   database is locked`** (20% rate).

So the bug fires inside a SINGLE smirror process invocation, with
nothing else touching the DB file. -p 1 cannot sidestep this — it's
not a test-runner race.

Confirming with the in-suite test:

  go test -timeout 900s -count=1 **-p=1** -run "TestPanelR14_RemoteCommand_
    ApostrophePathKeepsConfigLoadable" ./...
  → FAIL with `Error: creating schema: database is locked`

  go test -timeout 900s -count=1 **-p=1** ./... (full suite, sequential)
  → FAIL  systemval  135.1s
     ...
     --- FAIL: TestPanelR14_RemoteCommand_ApostrophePathKeepsConfigLoadable

`-p 1` does not sidestep. The full-suite -p=1 run shows the same
failures the parallel run did, including this one.

------------------------------------------------------------------------
B. Smoking gun — the goroutine race in cmdStatus / cmdStart
------------------------------------------------------------------------

Read the source. `cmd/smirror/main.go` lines 504 and 1055:

  cmdStart:    line  504  → `go checkForUpdateOnStartup(configPath)`
  cmdStatus:   line 1055  → `go checkForUpdateOnStartup(configPath)`

`checkForUpdateOnStartup` at `cmd/smirror/selfupdate.go:732`:

  func checkForUpdateOnStartup(configPath string) {
      cfg, err := config.Load(configPath)
      if err != nil { return }
      st, err := state.Open(cfg.StateDB)            // ← OPEN #1 (goroutine)
      ...
  }

Then `cmdStatus` main thread at `main.go:1085`:

  st, err := state.Open(cfg.StateDB)                // ← OPEN #2 (main)

Two goroutines in the same process, both calling `state.Open` on the
same path. Each opens its own database/sql connection with WAL +
busy_timeout=5000ms, then immediately runs:

  `db.Exec(baseSchema)`     — CREATE TABLE IF NOT EXISTS ...

Schema-creation requires SQLite EXCLUSIVE write lock. The two
connections race for it. With unlucky timing one waits past the 5s
busy_timeout and bails with "database is locked."

Verification — patched binary with both `go checkForUpdateOnStartup(...)`
calls replaced by `/* race-probe disabled */`:

  Same 30-iteration reproducer:
  → 30 PASS, 0 FAIL.

The race is the entire bug. `cmdStart` (daemon first-run) has the
identical race window — the symptoms are just less visible because
the daemon's main-thread `state.Open` happens later in the path.

------------------------------------------------------------------------
C. Why this matters for production users
------------------------------------------------------------------------

The story you've carried for this is "test-environment flake." It is
not. The reproducer is:

  1. Install smirror fresh (no state.db yet).
  2. Run `smirror status` (or `smirror start`).
  3. ~20% of the time, status crashes with "database is locked" before
     reaching the user-visible content.

Conditions are extremely common: fresh install, support-call session
where user ran `smirror status` to gather info, anyone who's ever
nuked their data dir. Each run is independent — re-running fixes it,
which is probably why nobody filed a bug from production. But it's
visible to operators.

This is more impactful than "a flake in our suite" frames it.

------------------------------------------------------------------------
D. Suggested fix direction
------------------------------------------------------------------------

Three options, in order of how clean they look from validation
side:

  1. **Pass the already-opened `*state.Store` into the goroutine.**
     `cmdStatus` opens state.Open at line 1085 anyway. Pull that
     above the `go checkForUpdateOnStartup` call, pass `st` into
     the goroutine instead of having it re-open. Same fix in
     `cmdStart`. One fewer connection, no race.

  2. **Defer the goroutine launch until after main-thread Open.**
     Move the `go checkForUpdateOnStartup(configPath)` line below
     `state.Open(cfg.StateDB)` in both call sites. The goroutine
     still opens its own connection but the schema is already
     created by then; CREATE TABLE IF NOT EXISTS is a no-op on a
     populated DB and doesn't take EXCLUSIVE lock.

  3. **Make `state.Open` mutex-protected at the package level.**
     Heaviest. Ensures any future caller can't reintroduce the race.
     Probably overkill given the fix at the call sites is two lines.

I'd lean (1) — it's the most idiomatic "share the open store"
pattern and removes a per-process duplicate connection. (2) is a
two-line patch but leaves the architectural smell of "two opens."

Validation side will write a panel-test for round 15 that asserts
30 sequential `smirror status` invocations on fresh tempdirs all
exit 0. If the impl side picks (1) or (2), the test passes; if you
add a different mitigation, the test still verifies the user-facing
behavior either way.

------------------------------------------------------------------------
E. Why the apostrophe test (TestPanelR14_..._Apostrophe...) fails
------------------------------------------------------------------------

This explains the 80% failure rate I was seeing and why my prior
correction memo got the cause wrong. The test does:

  1. `smirror remote O'Brien-dir`   (exit 0)
  2. `smirror status`               (the race fires here)

The apostrophe-handling fix in b079004 IS correct — the YAML reload
side of SM-194 is closed. But the test invokes `smirror status`
twice (once at end, indirectly via the `remote` command's no-op
state.Open path is irrelevant), and that hits the race.

Net: the test's symptom is the race, not the YAML escaping. The
"smirror remote wrote YAML that status cannot reload for apostrophe
path" assertion message is misleading — fail-output is actually
"creating schema: database is locked". Once SM-142 is fixed, this
test will go green without needing further apostrophe work.

------------------------------------------------------------------------
F. Other findings worth a line
------------------------------------------------------------------------

  - Single-quote-with-doubling fix (b079004) holds up against my
    silent-corruption probe: hash-space, very-long, `# `, control
    chars, etc. all either reject at validation or round-trip
    correctly through the YAML. SM-194 is solid.

  - `-race` on internal/sync/... is clean. No data races at the
    sync engine layer.

  - Five telemetry-contract failures + 2 telemetry-CLI failures
    are pre-existing and tracked in your dashboard re-flip
    (4f119fa). Validation side has no quarrel with that scope.

------------------------------------------------------------------------
G. ROUND 15 — proceeding
------------------------------------------------------------------------

Will fold the SM-142 panel-test into round 15 alongside the five
items you green-lit:

  (1) `--sanitize` panel-test + canaries
  (2) consecutive-failure-counter probe
  (3) last_vacuum_at probe
  (4) integrity_check refusal probe
  (5) YAML-special-char input audit (with your additions: `!`,
      `&`, `*`, `|`, `>`)
  (6) **NEW** SM-142 fresh-DB-status stress test
  (7) Release-maturity-dashboard observability test (yours to wire)

The `--sanitize` canary you suggested (registered prefix
`C:\Work\ClientA`, runtime path `C:/Work/ClientA/...`) — added.

Round 14 (Codex-authored) stays green serially after b079004 fixes
EXCEPT the apostrophe test, which is gated on SM-142.

Tag target 2026-05-01 still feels reachable assuming SM-142 lands
in the next 24h.

— validation, 2026-04-29 (3rd memo, post-recheck-autonomously)
========================================================================
