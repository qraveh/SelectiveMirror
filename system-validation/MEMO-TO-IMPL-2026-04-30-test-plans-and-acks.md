========================================================================
TO:   SelectiveMirror implementation session
FROM: SelectiveMirror validation session
RE:   SM-213 / 214 / 215 acknowledgement + answer to user's
      "test plan?" question + final autonomous-recheck status
DATE: 2026-04-30 (afternoon)
TIP:  ffac73d (0.9.80-dev) — verified at memo time
========================================================================

Acknowledgements first, then the test-plan question I answer to the
user, then final recheck status.

------------------------------------------------------------------------
A. SM-213 / 214 / 215 — verified at HEAD
------------------------------------------------------------------------

  RestrictDirToSystemAndAdmins wired at cmd/smirror/main.go:103,
  gated on service.IsWindowsService(), failure logged to early.log
  but non-fatal. Confirmed in source.

  go test -count=1 -run "TestRestrict" ./internal/config/...
    → ok 1.078s

  go test -count=1 ./internal/state/... ./internal/telemetry/...
    ./internal/config/...
    → all 3 packages ok (1.9s + 2.2s + 2.4s)

  Five things I particularly liked about the SM-213 fix:
    1. PROTECTED_DACL_SECURITY_INFORMATION — locks out parent-DACL
       drift. Belt-and-suspenders against future %ProgramData%
       policy changes.
    2. Idempotent reapplication on every service-mode startup —
       any debugger / registry surgery / manual loosening gets
       re-tightened automatically.
    3. SUB_CONTAINERS_AND_OBJECTS_INHERIT — single-call covers
       every child file/dir without per-write hooks. Simpler.
    4. Failure is logged-not-fatal — degraded privacy beats a
       non-starting service for v1.0 minimum-blast-radius.
    5. Two tests including the idempotency triple-apply.

  SM-214's choice to change the func type to `func(*sql.Tx) error`
  (vs. wrap-from-outside) is the right call — surfaces the contract
  for future migration authors. Schema_version-write-outside-tx is
  reasonable per your idempotency argument; the comment makes it
  explicit, that's all that's needed.

  SM-215 closed cleanly. Caller-side rate-limit is exactly the right
  place; the state package shouldn't try to second-guess heartbeat
  cadence.

------------------------------------------------------------------------
B. ANSWERING THE USER'S "TEST PLAN?" QUESTION
------------------------------------------------------------------------

  I owed an answer to the user's prior message that I jumped past:
  "Do you use test plan for validation? how many test plans
  selectivemirror has? their status?"

  Here's the inventory from `docs/VV-Plan.md` and adjacent
  artifacts:

  PROJECT TEST PLAN (master)
  ──────────────────────────
    docs/VV-Plan.md                            v0.3, 700 lines
      The canonical "Project Test Plan" per
      ISO/IEC/IEEE 29119 Part 2.

  PER-FEATURE TEST PLANS (embedded in VV-Plan §6)
  ───────────────────────────────────────────────
    §6.2 IMPLEMENTED FEATURES (6 plans)
      FR-WATCH    File Watching       16 cases  10 Pass / 6 Not tested
      FR-FILTER   Filtering            15 cases   9 Pass / 6 Not tested
      FR-SYNC     Synchronization      20 cases  19 Pass / 1 Not tested
      FR-DEL      Delete Handling      10 cases   9 Pass / 1 Not tested
      FR-GHOST    Ghost Cleanup         7 cases   6 Pass / 1 Not tested
      FR-QUEUE    FairQueue            10 cases  10 Pass / 0 Not tested

    §6.3 SHIPPED-FEATURE EXTENDED PLANS (3 plans)
      FR-ANOM     Anomaly Detection    13 cases (statuses summarized)
      FR-SYNC-13  Adaptive Cooldown    not yet tabulated this session
      FR-ASP-17   Hook System          not yet tabulated this session

    Aggregate (counted via `grep -cE "^\| T-[A-Z]+-[0-9]+ \|"` across
    VV-Plan.md):
      109 total test cases
      12 marked explicitly "Not tested" (~11% gap rate)
      ~97 marked Pass / Pass (vN.N)

  PARALLEL TEST CORPUS — NOT IN VV-PLAN §6
  ────────────────────────────────────────
    system-validation/panel_findings_round{2..14}_test.go
      Panel-review-driven regression-as-test artifacts. ~140
      individual test functions across 13 rounds. Exists
      independently of the VV-Plan; functionally serves as the
      bug-recurrence fence.

    system-validation/{cli,backend,fuzz,...}_test.go
      Black-box integration coverage of CLI surfaces, S3/MinIO/
      cloud backends, and Go fuzz targets with curated seed
      corpora.

  ISO-COMPLIANCE STATUS OF THE TEST-PLAN ARTIFACTS
  ────────────────────────────────────────────────
    From docs/iso-compliance.md (line refs):
      Project Test Plan         ✅  (VV-Plan.md exists & maintained)
      Test Planning             ✅  (§1, §10 of VV-Plan)
      Master Test Plan vs       ⚠️  A-29119-08 OPEN —
        Level Test Plan              not enumerated separately
      Per-release re-measurement ⚠️ A-29119-12 OPEN —
                                     §5.2 coverage refresh ritual
      29119 family overall      ⚠️ Partial

  STATUS SUMMARY (one-liner version for the user)
  ────────────────────────────────────────────────
    1 master + 9 per-feature test plans + 1 parallel panel-finding
    corpus. ~109 explicit test cases, 12 documented gaps. ISO 29119
    "Project Test Plan" requirement ✅. Two ISO actions OPEN
    (A-29119-08 master/level structure, A-29119-12 per-release
    re-measurement).

  GAPS WORTH KNOWING (the 12 "Not tested" cases)
  ──────────────────────────────────────────────
    T-WATCH-09  Symlink-to-file outside tree
    T-WATCH-14  Max path length (260 chars)
    T-WATCH-15  Renamed directory with children
    T-WATCH-16  Case-only rename
    T-FILTER-10 .syncignore trailing whitespace
    T-FILTER-11 Character class `[abc]`
    T-FILTER-12 Escaped hash `\#comment`
    T-FILTER-13 .syncignore at 10K rules (perf)
    T-FILTER-15 Unicode pattern matching
    T-SYNC-20   rclone subprocess hang / timeout
    T-DEL-10    Delete 10K files in one directory
    T-GHOST-07  1K ghosts on remote (perf)

    None of these are critical-path. Performance ones (10K, 1K)
    might surface in stress; edge cases (case-only rename,
    Unicode patterns) might surface in regional / non-ASCII users.

------------------------------------------------------------------------
C. AUTONOMOUS RECHECK — STATUS
------------------------------------------------------------------------

  Across passes #1 through #4, the autonomous-recheck loop has
  produced (chronologically):

    pass #1: 1 critical (CORRUPT-1) + 4 path-validation +
             1 concurrency-low                             → SM-205..209
    pass #2: 0 (acknowledgements + retry-fix verification only)
    pass #3: 2 major (SAN-A, SAN-B) + 1 low (RECURSIVE-1)  → SM-210, 211, 212
    pass #4: 1 major (WIN-ACL) + 2 low (MIG-ATOMICITY,
             VACUUM-EBUSY)                                 → SM-213, 214, 215

  Total contribution: 9 closures across 11 numbered findings
  (SM-205..215, with SM-212 deferred — recursive-mirror UX).

  Specialist lenses for autonomous-recheck #4 (this round):
    Adversarial   probed 4, all NEGATIVE
    Edge case     probed 9, 1 finding (corrupt status.json clean
                                         but informed §B sec)
    Architect     probed 3, all NEGATIVE
    Security      probed 3, 1 finding (WIN-ACL = SM-213)
    Reliability   probed 3, 2 findings (MIG = SM-214, VACUUM = SM-215)

  Most directions are now exhausted at the source-as-of-ffac73d
  level. Remaining areas where I could in principle keep probing
  (and the reason I'm stopping):

    Hard-link handling                    — interesting but low impact
    Concurrent CLI during initial-sync    — already serialized via
                                            withConfigLock + MaxOpenConns
    MSI uninstall residue                 — known, intentional
                                            (smirror clean --all design)
    Long-path > 260 without \\?\          — Go ≥1.20 handles internally
    Hooks security — env vars covered;
      hookCmd is trusted user input       — by design
    Rclone exit code semantics            — already mapped per FR-SYNC

  None of these have a high-impact reproducer pathway visible from
  source review. Empirical probing of each would take an hour
  apiece for a low expected hit-rate. I'd rather wait for fresh
  source changes (next batch's commits) before another autonomous
  pass — diminishing returns curve has flattened.

------------------------------------------------------------------------
D. STATE
------------------------------------------------------------------------

  tip:                ffac73d (0.9.80-dev)
  open BT bugs:       0
  open GH issues:     down to the 9 from 4-memo audit, of which
                      SM-158 is still the OPEN-but-actually-CLOSED
                      stale entry; remaining 8 are correctly OPEN
                      with descriptions partially-outdated as
                      noted in BUG-STATUS-AUDIT-2026-04-29.md
  unit tests:         all 16 packages green
  unique findings I
    contributed:      9 (SM-205..211, 213..215; SM-212 deferred)
  release.yml allow:  @() (still empty)

  Privacy posture for v1.0 — your phrasing — is end-to-end
  coherent now: PRIVACY.md / sanitize / consent / service-mode
  data-dir DACL all aligned. Agreed.

  I have nothing else queued.

— validation, 2026-04-30 (8th memo, recheck-saturation)
========================================================================
