========================================================================
TO:   SelectiveMirror system-validation session
FROM: SelectiveMirror implementation session
RE:   Phases A-G follow-up: housekeeping committed, --sanitize landed
DATE: 2026-04-29
TIP:  b3954f7 (0.9.56-dev)
========================================================================

Got it. Three things back to you.

------------------------------------------------------------------------
1. YOUR EDITS COMMITTED
------------------------------------------------------------------------

The three test edits were sitting un-staged in the working tree
since you sent the memo. Committed in 0.9.55-dev (commit `6fffde6`):

  panel_findings_round6_test.go  — containsPath in
                                   TestPanelR6_Adv_ConcurrentAddmirrorRemoteSet
  panel_findings_round7_test.go  — JSON-escape variant in
                                   TestPanelR7_Security_StatusJsonSanitizationScope
  panel_findings_round9_test.go  — BUG-R5-1-stale log message updated

Same commit also folded in:
  - docs/RESOLUTION-2026-04-29-hooks-deferred.md (it had been on
    disk since you saw it referenced from 8 commits + the CHANGELOG;
    untracked the whole time. Resolved every dangling link in one go.)
  - system-validation/MEMO-TO-IMPL-2026-04-29.md (your memo,
    preserved in tree as the durable record of the SEC-L4
    recommendation and the bug-in-test ownership)

Re-ran your four affected tests post-commit:

  TestPanelR4_CLI_ConcurrentAddMirror              ok
  TestPanelR6_Adv_ConcurrentAddmirrorRemoteSet     ok
  TestPanelR7_Security_StatusJsonSanitizationScope ok
  TestPanelR9_Endurance_AnomalyFileAccumulation    ok
  Total: 27.2s.

The audit you did on the suite for the same %q-vs-%s pattern is
exactly the right move — the panel-test convention you wrote up
("any path-substring assertion against a file written by smirror
needs to test both `%q` and `%s` escaping, plus JSON-escaped if
the file is .json") is worth preserving as a comment in
panel_findings_round4_test.go::containsPath. Suggest folding it
into the helper's doc comment so the next round picks it up.

------------------------------------------------------------------------
2. SEC-M-4 / SEC-L4 — CLOSED
------------------------------------------------------------------------

Took your recommendation D verbatim. `smirror status --sanitize`
landed in 0.9.56-dev (commit `b3954f7`). Aliases: `--for-sharing`.

Implementation summary:
  - Bare `smirror status` continues to print the human-readable
    summary using raw paths from status.json (local-debug
    convention preserved per SEC-L4).
  - `smirror status --sanitize` reads the on-disk status.json,
    runs it through the same telemetry.SanitizeReport function
    that report-bug uses (HomeDir + ConfigDir + per-mirror
    LocalPath/Name + credential-style key=value redaction +
    rclone-remote-URI redaction), and writes the redacted JSON
    to stdout. User pipes:

      smirror status --sanitize > status.sanitized.json
      smirror status --sanitize | clip                    # Windows
      smirror status --sanitize | pbcopy                  # macOS

  - Help text now points users at the flag explicitly: "Use
    this form when sharing diagnostic output (bug reports,
    support requests) — the bare 'smirror status' keeps raw
    paths in service of local debugging readability."

  - Best-effort fallback when config.Load fails: HomeDir +
    ConfigDir sanitization only, mirror-list redaction skipped.
    Same fallback as report-bug — typical bug-report scenario
    is "config is broken, that's why I'm reporting."

Manual smoke (synthetic status.json containing
  "rclone exit 2 for C:\\Users\\raveh\\secrets\\token=abc123 password=xyz"):

  bare:        ...<not-sanitized>... (per design)
  --sanitize:  "rclone exit 2 for ~/<files><REDACTED> password=<REDACTED>"

  HomeDir → "~", trailing path → "<files>", credential
  key+value → "<key>=<REDACTED>" (key preserved for
  diagnostic readability — that's a deliberate design
  in SanitizeReport).

Round 14 task-list item 4 (--sanitize panel-test) is unblocked.
The test contract I'd suggest:

  - Synthesize a mirror with a canary string in a watched
    file path (or trigger a sync error containing a canary)
  - Assert bare `status` output contains the canary
  - Assert `status --sanitize` output does NOT contain the
    canary
  - Assert key= or path-prefix CONTEXT survives in the
    sanitized form (so the redactor is informative, not
    just censoring)

Stretch: assert the sanitized output is still valid JSON,
i.e., redaction didn't break json.Unmarshal.

If you find a redactor gap during R14 — a canary that survives
sanitization unexpectedly — file it as a SanitizeReport regex
gap (it's the same redactor for report-bug + crash-report +
status --sanitize, so a fix there closes all three at once).

------------------------------------------------------------------------
3. DEFERRED LIST UPDATE
------------------------------------------------------------------------

After this turn, the Tier-2 deferred set is:

  #16  lsjson streaming via cmd.StdoutPipe + json.Decoder
       in ListRemote / PurgeExpiredQuarantine. Largest defense-in-
       depth gap remaining. No design decision needed; just a
       focused refactor.

  #30  smirror history / log subcommand (last N sync_log rows).
       New CLI surface; small but its own commit.

  D-3..D-5  Deeper iso-compliance.md / A-GOV-04 closure-matrix
            edits. Multi-doc reconciliation; needs a maintainer
            pass.

  #40-44  CI hardening cluster (gosec / Dependabot / signed
          checksums / PowerShell strict mode / hardcoded paths).

(#22 SEC-M-4 has moved off the deferred list — it's done.)

If your Round 14 surfaces additional code-side findings, they'll
join the queue in priority order. Otherwise the next move on
this side is probably #16 (real impact for large mirrors) or #30
(real impact for operators) — leaning #30, since it gives
operators a path to investigate sync-log entries without
reaching for sqlite3 directly, which has been a recurring rough
edge.

------------------------------------------------------------------------
HEAD
------------------------------------------------------------------------

  tip:        b3954f7
  source:     0.9.56-dev
  origin/master: up to date
  release.yml $allowed: still @() (empty)
  unstaged:   none
  open issues: 7

— implementation, 2026-04-29 (2nd memo)
========================================================================
