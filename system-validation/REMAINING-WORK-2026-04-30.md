========================================================================
REMAINING WORK — comprehensive list as of tip ffac73d (0.9.80-dev)
DATE: 2026-04-30
========================================================================

Built from: open GitHub issues, iso-compliance.md OPEN actions, recent
memo deferral lists, VV-Plan §6 "Not tested" cases, my prior bug-
status audit, and the operator-side pre-tag checklist.

Organized by what action the item needs:

  §A  TO FIX — source / docs / config changes
  §B  TO VALIDATE — tests, probes, audits owed
  §C  SCOPE DECISIONS — outstanding judgment calls
  §D  PRE-TAG OPERATOR — release-day work (not bug-related)
  §E  v1.1+ DEFERRED — explicit post-v1.0 backlog

------------------------------------------------------------------------
§A. TO FIX — source / docs changes still pending
------------------------------------------------------------------------

  GitHub-tracked OPEN bugs (9):
    SM-160  #163  minor      Hooks deferred from v1.0 — TRACKER ONLY,
                              not an unfixed defect; closes when hooks
                              are promoted, removed, or v1.0 ships
    SM-159  #162  cosmetic   rclone 2.x classified as Full Compatibility
                              — `internal/rclone/detect.go:59` lacks
                              upper bound; suggested fix `if v.Major()
                              >= 2 { return CompatNone }`
    SM-158  #161  minor      "NEW-R10-1: zero anomaly files" — ALREADY
                              FIXED IN SOURCE (a198724, 0.9.48-dev),
                              recorded as CLOSED in iso-compliance.md
                              §10.6, but GitHub still shows OPEN.
                              ACTION: close issue with reference to
                              a198724.
    SM-143  #145  major      unmirror quoted-name regex — fixed in
                              471c5dc (the regex relaxed to
                              `['"]?<name>['"]?`); GitHub issue body
                              not yet updated to reflect closure.
                              ACTION: close issue with reference.
    SM-142  #144  major      SQLITE_BUSY self-race — symptom-level
                              fixed via retry loop in 471c5dc; in-
                              process race root cause remains.
                              ACTION: close issue OR re-scope to
                              "remove root-cause goroutine race"
                              (architectural follow-up).
    SM-082  #94   minor      4 error-handling gaps. PARTIALLY FIXED:
                              watcher.Close + UnlockFileEx now logged.
                              REMAINING:
                                (3) svc.Control inconsistency between
                                    service.go:194 (warn-and-continue
                                    in uninstall) and service.go:300
                                    (return error in stop)
                                (4) Anomaly Detail includes elapsed +
                                    exit-code but NOT stderr (logged
                                    via e.log.Warn at sync.go:1170,
                                    dropped from anomaly record)
                              ACTION: edit body to mark 1+2 closed,
                              keep 3+4 open.
    SM-073  #85   critical   sync-now/dry-run lock — SYNC-NOW FIXED
                              (acquires lock at main.go:785). dry-run
                              still doesn't acquire lock at main.go:
                              878-916. Closed-by-rationale per impl
                              session memo (dry-run is read-only,
                              WAL-safe). ACTION: close as documented
                              divergence OR update body to scope down
                              to dry-run-only with rationale.
    SM-057  #69   minor      Burst-delete reconciliation 30s sleep —
                              `internal/watcher/watcher.go:574`,
                              `burstReconcileDelay = 30 * time.Second`.
                              Comment in source already says
                              "should be quiescence-based". UNFIXED.
                              ACTION: implement quiescence-based
                              reconciliation OR close as v1.0.x defer.
    SM-042  #54   minor      Debounce test doesn't verify rclone
                              invocation count. `test/run_tests.ps1:
                              238-249` only checks final content.
                              UNFIXED. ACTION: add log-parse / rclone
                              invocation-count assertion to
                              Test-DebounceRapidWrites.

  Codex-tracked items (local BugTracker, not on GitHub):
    SM-157  ─── ─── doc      "smirror telemetry" CLI command is
                              documented in `docs/cli-telemetry-
                              command.md` (40+ references) but no
                              `case "telemetry":` in main.go and no
                              cmdtelemetry.go. The doc describes a
                              feature that doesn't exist. ACTION:
                              EITHER implement the command per docs,
                              OR mark the doc explicitly as "design
                              pending implementation in v1.1" and
                              update README references.

  Tier-2 deferred (from impl-session followup memo §3):
    Tier-2 #16    lsjson streaming via cmd.StdoutPipe + json.Decoder
                  in ListRemote / PurgeExpiredQuarantine. Largest
                  defense-in-depth gap remaining. Focused refactor.
    Tier-2 #30    `smirror history` / `smirror log` subcommand — last
                  N sync_log rows. New CLI surface; small commit.
    Tier-2 D-3   ─5 Deeper iso-compliance.md / A-GOV-04 closure-matrix
                    edits. Multi-doc reconciliation; needs maintainer
                    pass.
    Tier-2 #40-44 CI hardening cluster (gosec / Dependabot / signed
                  checksums / PowerShell strict mode / hardcoded
                  paths).

  Documentation drift (from prior BUG-STATUS-AUDIT-2026-04-29.md):
    docs/release-maturity.md:19  language "TestCLI_Status SQLITE_BUSY
                                  parallel-load flake (SM-142)" —
                                  "parallel-load" wording was already
                                  retracted in BugTracker but still
                                  in the dashboard text. ACTION:
                                  update one line.

------------------------------------------------------------------------
§B. TO VALIDATE — tests, probes, audits owed by validation side
------------------------------------------------------------------------

  Round 15 panel-test commitments (never written; I owe these):
    1.  `--sanitize` panel-test with SM-179 / SM-195 / SM-210 / SM-211
        canaries: bare status leaks, --sanitize redacts; mixed-case +
        case-fold + word-boundary canaries.
    2.  `consecutive_full_sync_failures_<project>` meta-key probe:
        fresh seed = 0; +1 per failed sync-now; reset to 0 on
        success; KindCircuitBreaker fires at threshold = 3.
    3.  `last_vacuum_at` meta-key probe: heartbeat populates after
        vacuumInterval (7 days); subsequent ticks within window
        leave it alone.
    4.  integrity_check refusal probe: corrupt state.db (truncate
        mid-page), assert state.Open returns refusal error.
    5.  YAML-special-char input audit for addmirror: `'`, `"`, `:`,
        `\`, leading `-`, leading `#`, `!`, `&`, `*`, `|`, `>`.
    6.  SM-142 fresh-DB-status stress test: 30 sequential
        `smirror status` invocations on fresh tempdirs all exit 0.
    7.  Release-maturity-dashboard observability test: validation-
        coverage report cross-checks test status before declaring
        "goals met". (Impl wires the source change after the test
        exists.)

  VV-Plan §6 documented "Not tested" gaps (12 cases, none critical-
  path):
    T-WATCH-09   Symlink-to-file outside tree
    T-WATCH-14   Max path length 260 chars
    T-WATCH-15   Renamed directory with children
    T-WATCH-16   Case-only rename (File.txt → file.txt)
    T-FILTER-10  .syncignore trailing whitespace
    T-FILTER-11  Pattern with character class `[abc]`
    T-FILTER-12  Escaped hash `\#comment`
    T-FILTER-13  10K rules (perf)
    T-FILTER-15  Unicode pattern matching
    T-SYNC-20    rclone subprocess hang / timeout
    T-DEL-10     10K-file delete in one directory
    T-GHOST-07   1K ghosts on remote (perf)

  Validation probes flagged but not fully exhausted:
    Hard-link handling — two relPaths under same project, same inode;
                         likely each gets its own sync_state row,
                         dedup at content-hash level. Worth a unit
                         test in v1.1.
    Concurrent CLI mutations during initial-sync — addmirror
                         --initial-sync racing against unmirror.
                         withConfigLock guards config edits;
                         MaxOpenConns=1 serializes state.db writes.
                         Probably already safe but not formally
                         tested.

  Cloud backend coverage (blocked on credentials):
    B2          — needs B2 application key
    Drive       — needs OAuth credentials
    OneDrive    — needs OAuth credentials
    SFTP        — could be tested with localhost OpenSSH server
    MinIO local — already covered in `_cloud_test.go`

------------------------------------------------------------------------
§C. SCOPE DECISIONS — outstanding judgment calls
------------------------------------------------------------------------

  release.yml gate-scope decision (operator-side, but informs
  validation-side panel-test scope):
    Option A: panel-tests-only gate (current — release.yml $allowed
              @() empty for panel-finding regression suite)
    Option B: allowlist-the-8-telemetry-tests as known-issue and
              gate full system-validation suite

  How to handle the 6 telemetry contract failures (telemetry v2
  architecture rewrite at aca5ba5 made the v1 static-content tests
  stale):
    TestTelemetryServerContract_IngestNormalizesAcceptedEvents
    TestTelemetryServerContract_WorkerExposesForgetEndpoint
    TestTelemetryWorker_PrivacyAndEdgeLimits
    TestTelemetryRLS_ServerOwnedColumnsCannotBeClientSet
    TestTelemetryRLS_EnvelopeFieldsAreAuthenticated
    TestTelemetryRetention_PurgesNormalizedRawText
  PLUS: 2 telemetry-CLI tests for the absent `smirror telemetry`
    command (TestTelemetryCLI_DefaultNoneStatus, TestTelemetryCLI_
    TierTransitionPersists)
    Decision: rewrite tests for v2 schema, OR delete the v1 tests
    and add v2 tests post-tag, OR ship with allowlist + known-issue
    note.

  SM-198 burst-budget reclassification — already done (76bf2f6
  changed t.Errorf to t.Logf), but the path-A vs path-B decision
  for v1.0.x remains: either fix live-sync throughput OR raise
  harness sync_workers default and re-baseline.

  SM-212 (recursive mirror UX) — deferred per my own recommendation;
  could re-open if v1.0.x has spare cycles. rclone catches at
  runtime, no destructive cycle, just sync-failure log noise.

------------------------------------------------------------------------
§D. PRE-TAG OPERATOR — release-day work (not bug-related)
------------------------------------------------------------------------

  From every batch-N memo's "Operator-side remaining" footer
  (unchanged from batch5 through batch7):

    1. release-dryrun.yml run against current HEAD
    2. R-5 MSI smoke test re-run with the b346dcd-patched harness,
       elevated terminal
    3. CHANGELOG `[1.0.0]` cleanup (duplicated paragraph + stale
       "0.9.66-dev → 1.0.0" historical wording)
    4. release.yml gate-scope decision (see §C above)
    5. sm-keeper Mode B: bump 0.9.80-dev → 1.0.0, tag, push

  External blockers (out of project's control):
    - SignPath Authenticode certificate (in flight; gates
      SmartScreen friction on first install — currently 🔴 in
      docs/release-maturity.md row "Code signing")
    - winget submission to microsoft/winget-pkgs (manual or via
      auto-submit gate `WINGET_SUBMIT_ENABLED=1`)

------------------------------------------------------------------------
§E. v1.1+ DEFERRED — explicit post-v1.0 backlog
------------------------------------------------------------------------

  ISO/IEC/IEEE 29148 (Requirements) — 17 OPEN actions:
    A-29148-01  Verification Method column on FR-* tables (P0)
    A-29148-02  In-document change-history table on SRS.md (P2)
    A-29148-03  Named approval / sign-off block on SRS cover (P2)
    A-29148-04  "Source / Origin" column on NFR tables (P3)
    A-29148-05  Define verification method per FR (P0)
    A-29148-06  "Target / SLA" → "Acceptance Criteria" (P2)
    A-29148-07  docs/conops.md (Concept of Operations) (P2)
    A-29148-08  TestFR_XXX_YY_Scenario naming convention (P1)
    A-29148-09  Auto requirement→test traceability matrix (P1)
    A-29148-10  Audit FR-* for implementation leakage (P3)
    A-29148-11  Split compound requirements (P3)
    A-29148-12  Convert subjective FRs to measurable form (P1)
    A-29148-13  Tag verification method as I/A/D/T per 29148 §6.4 (P2)
    A-29148-14  Fix VV-Plan.md §1.1 V&V table (integration =
                verification, not validation) (P1)
    A-29148-15  Stakeholder list, Glossary split, Distribution list (P2)
    A-29148-16  Formal Safety justification (P3)
    A-29148-17  User Documentation Requirements section in SRS (P1)

  ISO/IEC 25010 (Quality model) — 9+ OPEN actions:
    A-25010-02  Replace "Usability" with "Interaction Capability"
                (P0; not waivable while claiming 25010:2023)
    A-25010-03  Annotate FR-DEL-01 three-policy as Functional
                Appropriateness evidence (P2)
    A-25010-05  Add Authenticity NFR (rclone-mediated) (P1)
    A-25010-06  Promote security-audit-2026-04-18 findings into
                formal Resistance NFR(s) (P1)
    A-25010-07  Close Reusability evaluation: hook surface as
                evidence (P3, due 2026-05-15)
    A-25010-08  Add Analysability NFR (P2; evidence accumulating)
    A-25010-10  Privacy / Data Protection NFR family (P1)
    A-25010-12  Reclassify Replaceability as architectural NFR (P1)

  ISO/IEC 25023 (Measurement) — 6 OPEN actions:
    A-25023-01  Author measurement-function table per quantitative
                NFR (P1)
    A-25023-02  Per-NFR measurement campaign — DEFERRED to v1.1
                R-19
    A-25023-03  Define availability-measurement methodology (P2)
    A-25023-04  Extend VV-Plan §2.3 with 25023 §5.2 attributes (P1)
    A-25023-05  Classify measurements as Base/Derived/Indicator (P2)
    A-25023-06  Eliminate "Met at looser value" framing in §4 (P1)

  ISO/IEC/IEEE 29119 (Testing) — 2 OPEN actions:
    A-29119-08  Master Test Plan vs Level Test Plan structure +
                Test Readiness Review checklist + Regression
                Approach + Test Data Requirements (P2)
    A-29119-12  Per-release VV-Plan §5.2 re-measurement ritual
                (already promoted to CI gate per v0.6 entry,
                refined ritual deferred)

  Other v1.1 items recorded in iso-compliance.md §10.6 / 1.0
  baseline row:
    R-12  Full ISO/IEC 25023 §5.2 measurement-function elaboration
          (deferred to v1.0.1)
    R-18  ISO/IEC 25023 §5.2 elaboration — long-form (v1.1)
    R-19  A-25023-02 measurement campaign (v1.1)
    R-21  SM-NNN single-source-of-truth migration (v1.1) —
          collapse GitHub vs local BugTracker namespace collision
    R-22  29148:2018 §9.5.5 doc-attribute gaps (A-29148-02/-03/
          -07/-15/-17) (v1.1)

  Watcher coverage refactor (X-04): mostly closed at 59.3% (v1.0
  target floor 60%). Full refactor to ~75-80% deferred to v1.0.1.
  Optional 2-hour top-up tests for `isLinkToDir` + `WatchCount`
  could nudge it over 60% before tag.

  WiX MSI permission element: deferred — MSI doesn't currently
  pre-create the data dir; runtime does, and SM-213's runtime
  RestrictDirToSystemAndAdmins covers it.

  Service-account-specific ACE: only relevant if smirror ever
  supports gMSA / virtual-account installs (currently LocalSystem
  only; SYSTEM ACE covers).

------------------------------------------------------------------------
SUMMARY — what's blocking v1.0 today vs what defers
------------------------------------------------------------------------

  HARD BLOCKERS for v1.0 tag:                                 0
  SOFT BLOCKERS (operator could ship with known-issue):       3
    - 6+2 telemetry test failures (test-update items, not source bugs)
    - SM-158/143/142 GitHub-issue housekeeping (state mismatch)
    - SM-202-style doc drift on release-maturity.md:19

  PRE-TAG OPERATOR WORK:                                       5 items (§D)

  DEFERRED-BY-DECISION:
    v1.0.x     ~7 items (Tier-2 #16, #30, D-3..D-5, #40-44 cluster,
                          plus T-* gaps if cycles)
    v1.0.1     X-04 watcher-coverage full refactor + R-12
    v1.1+      32 ISO actions (17 + 9 + 6 across 29148, 25010,
                25023) + R-18..R-22

  Bug closure is no longer a constraint on v1.0 tag (per impl
  session's batch7 memo).

— validation, compiled 2026-04-30
========================================================================
