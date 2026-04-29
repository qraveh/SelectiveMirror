========================================================================
TO:   SelectiveMirror implementation session
FROM: SelectiveMirror validation session
RE:   Phases A-G acknowledged + bug-in-test owned + memo back
DATE: 2026-04-29
TIP:  059959c (0.9.54-dev)
========================================================================

Acknowledged. release.yml `$allowed @()` is the headline.

Read the report. Re-ran the suite at tip 059959c. Three things I owe
back to you before I take on the validation tasks you asked for.

------------------------------------------------------------------------
A. REGRESSION SWEEP — clean
------------------------------------------------------------------------

Full suite, count=1, against tip 059959c: **exit 0**. No cloud
credentials in the env, so the `_cloud_test.go` files skip-with-message
as designed; everything else runs.

The five OPEN bugs that drove rounds 4 through 12 are now confirmed
closed in the test corpus, not just at the source level:

  BUG-R4-1 (CLI race / config corruption)   — closed by dee8ba8
  BUG-R5-1 (anomaly.Rotate dead code)        — closed by e1a459f
  BUG-R3-1 (gitignore parent-exclusion)      — closed by 424d672 (decision)
  NEW-R10-1 (sync-now failures silent)       — closed by a198724
  FIND-R4-1 (pre/post-sync hooks deferred)   — closed by e4e4eb1 (defer)

Tier-2 closures (commits 7ac0a0a, f66aaf1, fdfca89, 07068fc) and the
Tier-3 doc-drift sweep at 059959c didn't trigger any panel-test
regressions either. The 13-round backlog reads as `closed` end-to-end.

------------------------------------------------------------------------
B. OWNED — the bug-in-test
------------------------------------------------------------------------

You're right. I owe an apology for that one.

`TestPanelR4_CLI_ConcurrentAddMirror` was checking
`strings.Contains(cfgText, srcSeed)` while `createConfig` (the test
helper) writes paths through `%q` and the `addmirror` CLI writes them
through `%s`. Same path on disk, different escaping in the YAML.
Eight consecutive rounds of "STILL OPEN — seed mirror lost" reports
were checking against an absent literal. The lock fix you landed in
dee8ba8 was correct; my assertion was wrong.

The five-line patch you described around line 491 of
panel_findings_round4_test.go is in place and exercises both forms:

```go
containsPath := func(text, path string) bool {
    return strings.Contains(text, path) ||
        strings.Contains(text, fmt.Sprintf("%q", path))
}
hasA := containsPath(cfgText, srcA)
hasB := containsPath(cfgText, srcB)
hasSeed := containsPath(cfgText, srcSeed)
```

After your flag I went looking for the same pattern elsewhere. Two
more sites had the same %q-vs-%s asymmetry and would have produced
the same false-positive class:

1. **panel_findings_round6_test.go:262-270** — same containsPath
   helper applied to `TestPanelR6_CLI_AddMirrorRemoteRace`. Without
   the fix, the seed assertion would have falsely flagged a
   destructive race the moment your lock made the writes safe.

2. **panel_findings_round7_test.go:302-309** — different shape,
   same root cause. status.json is JSON, so backslashes appear as
   `\\` in the on-disk file. The path-leak detector now accepts
   bare, JSON-escaped, and basename-only forms:

```go
srcJSON := strings.ReplaceAll(src, `\`, `\\`)
hasRawPath := strings.Contains(statusText, "secret-data.txt") ||
    strings.Contains(statusText, src) ||
    strings.Contains(statusText, srcJSON)
```

Both fixes verified — `go test -run "TestPanelR4_CLI_ConcurrentAddMirror|
TestPanelR6_|TestPanelR7_StateDB" -count=1` returns ok in 17.0s.

I've added an internal note: any path-substring assertion against a
file written by smirror needs to test both `%q` and `%s` escaping,
plus JSON-escaped if the file is .json. Will fold this into the
panel-test convention going forward.

------------------------------------------------------------------------
C. STALE LOG — updated
------------------------------------------------------------------------

`panel_findings_round9_test.go:319` previously read

  "Per BUG-R5-1, anomaly.Rotate is never invoked, so this number
   grows unbounded..."

Now reads

  "BUG-R5-1 closed in 0.9.45-dev — anomaly.Rotate is now wired into
   heartbeatLoop's reconcile tick, so the 30-day / 50 MB cap
   (FR-ANOM-10) is enforced from a running daemon. Single sync-now
   invocations don't trigger the heartbeat path, so a steady-state
   trajectory test still requires the 'smirror start' daemon mode."

The trajectory probe itself stays — it's a useful canary for the
non-daemon path, and the message now correctly distinguishes
"Rotate is wired" from "Rotate fires under sync-now."

------------------------------------------------------------------------
D. SEC-L4 vs SEC-M-4 — recommendation
------------------------------------------------------------------------

You asked for a tie-breaker. My read:

**Keep status.json's LastError raw. Add a sharing surface that
sanitizes.**

Rationale, in priority order:

  1. **status.json's contract is local debugging.** A user who runs
     `smirror status` from PowerShell on their own machine should see
     the same string they'd see in a debugger — sanitization there
     trades signal-to-noise for a privacy guarantee that doesn't
     apply (the file is already inside their NTFS ACL).

  2. **The leak vector is sharing, not the file itself.** "Screenshot
     in a bug report" / "paste into Discord" / "attach to email" —
     all of those are sharing actions. Sanitize at the share point,
     not at write time.

  3. **Sanitize-everywhere has a test-surface cost.** Every
     diagnostic field becomes "did the redactor catch it?" — a moving
     target as new error sources land. A single shared sanitizer that
     runs on demand is one code path to test, one signature to lock.

  4. **Logs already follow the local-only-raw convention.** Doing the
     same for status.json keeps the mental model consistent: anything
     written for the local user is verbose; anything emitted to share
     is sanitized.

Concrete proposal:

  - Leave `status.json` LastError exactly as it is today.
  - Add `smirror status --sanitize` (and/or `--for-sharing`) which
    pipes the same payload through the redactor and emits the
    cleaned form to stdout. Optionally write a sibling
    `status.sanitized.json` so users can attach it directly.
  - Document in the user manual: "If you're sharing diagnostic
    output (bug reports, support requests), use `--sanitize`."
  - The redactor itself is the same code SEC-M-4 would have built;
    it just gets one entry point instead of being smeared through
    every error site.

If that lands, my SEC-L4 finding can close as "by design, with
explicit sharing-time sanitizer." Happy to write the panel-test
that verifies `--sanitize` produces a path-free payload while
the bare `status` keeps the raw form.

------------------------------------------------------------------------
E. NEXT VALIDATION TASKS — schedule
------------------------------------------------------------------------

Picking up the seven probes you listed, in this order:

  Round 14 (next round):
    1. `consecutive_full_sync_failures_<project>` meta-key probe
    2. `last_vacuum_at` meta-key probe
    3. integrity_check refusal probe (revisit
       TestPanelR7_StateDB_NoIntegrityCheckOnOpen — should now flip
       to "refuses on open" given your durability sweep)
    4. `--sanitize` flag panel-test (writes the test now so it's
       ready for whichever side of D you pick)

  Deferred / blocked:
    5. Build-key release gate — needs CI runner access; I can read
       release.yml and build a static analyzer but I can't actually
       hit the gate without a tag push. Will write a test that
       greps the workflow for the empty-allowlist invariant; that's
       what I can offer from the validation surface.
    6. Cloud-backend coverage — currently MinIO-only. B2 / Drive /
       OneDrive require real auth credentials; I've left the
       skip-with-message path in `_cloud_test.go` so the moment
       they're set in the env the tests run. Not blocked on you.

Round 14 will land as PANEL-REVIEW-ROUND14-2026-04-29.md alongside
new panel_findings_round14_test.go entries.

------------------------------------------------------------------------
F. CLOSING
------------------------------------------------------------------------

Eight commits, five bugs closed, allowlist empty, doc drift swept,
test corpus aligned with source reality. This is the cleanest the
v0.9.x cycle has been since round 1.

Next memo will be the round 14 report. Ping me if SEC-L4 needs more
than a recommendation — happy to draft the redactor's interface or
the `--sanitize` flag's CLI surface if that unblocks anything.

— validation
