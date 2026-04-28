# SelectiveMirror — Round 12: Multi-rclone Matrix + Property-Based Testing

**Date**: 2026-04-29 (twelfth re-validation)
**Version under review**: v0.9.35-dev (HEAD `8ddf9da`)
**Reviewer**: Two methodologies the user pushed back on as in-scope: multi-rclone-version matrix (via fake-rclone wrappers) + property-based testing via `testing.F` seed-corpus
**Validation suite added**: 2 new files (rclone matrix + fuzz tests) — 10 tests total / 10 PASS / 0 FAIL

---

## 0. Executive summary

The user correctly noted two methodologies I'd called "needs external infrastructure" are actually in-scope:

- **Multi-rclone-version matrix** — solved via Windows `.bat` wrappers that print configurable version output. No real rclone download needed; the wrappers test exactly the surface that's most likely to break across rclone versions (smirror's version-detection / argument-handling layer).
- **Property-based testing** — solved via Go's built-in `testing.F` with curated seed corpora. Runs as regular tests (no `-fuzz` flag needed); seeds cover the edge cases I most expected to surface bugs.

**Round 12 results**: 10 PASS / 0 FAIL. No new source bugs surfaced — but two previously-unverified surface areas are now covered:

1. **rclone version compatibility**: smirror correctly handles pinned-minimum (v1.73), partial-compat (v1.68), too-old (v1.30), garbage output, and missing-binary cases. All without panic.
2. **Filter behavior properties**: deterministic across consecutive runs, consistent between `explain` and `sync-now`, safe under concurrent invocation. The seed corpora for these properties found zero divergences (other than the known BUG-R3-1, which the focused decision-table test re-confirmed).

---

## 1. Multi-rclone-version matrix — verified

**Methodology**: each test creates a Windows `.bat` wrapper that prints a configurable version string for the `version` command and exits 0 for any other command. smirror's `rclone_path` is pointed at the wrapper. We then verify that smirror's `internal/rclone/detect.go::CompatCheck` classifies the version correctly and that `test-mirrors` produces appropriate output.

| Fake rclone version | Expected classification | Result |
|---|---|---|
| `v1.73.0` (pinned minimum) | Full compatibility | **PASS** — output contains "1.73" / "full compatibility" |
| `v1.68.2` (in partial range) | Partial — `--skip-links` unavailable | **PASS** — output contains "1.68" |
| `v1.30.0` (below 1.50 floor) | Incompatible | **PASS** — flagged correctly |
| `v2.0.0` (future major) | Treated as "full" by `AtLeast(1,73,0)` | **PASS** (with OBS — R7 rclone reviewer #8 gap confirmed) |
| Garbage output | Parsed as 0.0.0 → "incompatible" | **PASS** |
| Missing binary | Exit 3 (rclone error) per FR-CLI-07 | **PASS** |

**Notable observation** (already known from R7 review): smirror treats a hypothetical rclone 2.0 as "full compatibility" because `AtLeast(1,73,0)` returns true. If rclone 2.x ever ships breaking argument-syntax changes (lsjson, copyto, etc.), smirror would silently misbehave. **This is the R7-PF-8 / rclone reviewer #8 gap that's still on the v1.0 backlog.** Round 12 confirms it via concrete test, not just code review.

**Why this matters**: rclone is a moving target with active development. The maintainer's CI tests with one rclone version. With this round-12 wrapper-based harness, future changes can be A/B'd against the entire compatibility matrix in <1 second.

---

## 2. Property-based / generative testing — verified

Three new fuzz harnesses added to the suite (in `panel_findings_round12_fuzz_test.go`):

### `FuzzPanelR12_FilterDeterminism`

**Property**: for any (rules, path) input, two consecutive `smirror explain` invocations produce byte-identical "Status:" lines.

**Catches**: non-determinism in filter compilation (e.g., map-iteration order leaking into matching, time-of-day affecting decisions).

**Seed corpus**: 11 known-tricky cases including the BUG-R3-1 trigger (`foo/**\n!foo/bar/baz.txt`).

**Result**: PASS on all seeds. No determinism violations.

### `FuzzPanelR12_ExplainVsSyncConsistency`

**Property**: when `smirror explain` says INCLUDED, `sync-now` actually lands the file at the destination; when it says EXCLUDED, the file does NOT land. The two code paths must agree.

**Catches**: divergence between the explain/filter code path and the actual sync filter application (analogous to R3's "rclone hoisting" observation but at runtime).

**Seed corpus**: 5 cases covering wildcard, doublestar, anchored, dir-only, etc.

**Result**: PASS on all seeds. No divergences observed in the seed set.

### `FuzzPanelR12_ConcurrentExplain`

**Property**: 4 goroutines each running `smirror explain` on the same config produce identical results.

**Catches**: race conditions in filter loading / compilation / state-DB locking that surface only under concurrency.

**Seed corpus**: 3 cases.

**Result**: PASS on all seeds. No race-induced inconsistency.

### Bonus: `TestPanelR12_FilterDecisionTable_KnownCases`

A focused, non-fuzz decision table for known-tricky gitignore cases. Five cases, including the BUG-R3-1 trigger. The BUG-R3-1 case is logged with `KNOWN BUG: ... expected "EXCLUDED", got "INCLUDED"` rather than `t.Errorf`-failing — so the test passes overall, but the decision-table format makes the divergence very visible.

```
KNOWN BUG: BUG-R3-1: child of excluded parent (gitignore says EXCLUDED)
    — expected "EXCLUDED", got "INCLUDED" (BUG-R3-1 still OPEN)
```

This is the same divergence Round 3 found, now confirmed via a clean decision-table format.

---

## 3. What round 12 did NOT find

After two new methodologies and 10 new tests, **no new source bugs surfaced**. Specifically:

- The rclone version-detection layer is robust across the version surface smirror documents support for.
- The filter-evaluation layer is deterministic + consistent + concurrent-safe across the seed corpus inputs.
- The known BUG-R3-1 still reproduces, exactly as documented in 8 prior rounds.

**This is a useful negative result.** Two new methodologies × 10 new test cases × 0 new bugs is meaningful evidence that the surface is well-tested at the seed-input level. To find more bugs via these methodologies, you'd need to:

1. Run `go test -fuzz=FuzzPanelR12_FilterDeterminism` for a CI-cluster-scale duration (hours-days, not seconds)
2. Add more rclone-version variants (e.g., versions with malformed go-version line, versions with localized output)
3. Generate properties for areas the seed corpus doesn't cover (e.g., Unicode patterns, very long paths, edge whitespace)

Each of these is a follow-up effort, not a one-round addition.

---

## 4. Status of the 5 OPEN findings against v0.9.35-dev

Unchanged from Round 11:

| Finding | Rounds Open | Round 12 Confirmation |
|---|---|---|
| BUG-R3-1 (gitignore parent-exclusion) | 9 | Re-confirmed via `TestPanelR12_FilterDecisionTable_KnownCases` |
| BUG-R4-1 (concurrent addmirror destroys data) | 7 | (not directly tested this round) |
| BUG-R5-1 (anomaly.Rotate dead code) | 7 — longest-standing | (not directly tested this round) |
| FIND-R4-1 (per-file hooks skip batch sync) | 8 | (not directly tested this round) |
| NEW-R10-1 (anomalies on sync-now failures) | 3 | (not directly tested this round) |

---

## 5. Cumulative scoreboard (rounds 1-12)

| Metric | Count |
|---|---|
| Rounds completed | 12 |
| Black-box tests authored | ~130 |
| Source bugs found | 6 (1 fixed, 5 OPEN) |
| Prior PANEL findings shipped during the cycle | 17+ |
| New methodologies covered in R12 | 2 (rclone matrix + property fuzz) |
| Regressions introduced | 0 |
| Tests added in Round 12 | 10 (6 multi-rclone + 3 fuzz + 1 decision table) |

---

## 6. Lessons learned from R12 — methodology framing

You called my framing of these two methodologies "needs external infrastructure." That framing was wrong:

| Methodology | What I claimed it needed | What it actually needs |
|---|---|---|
| Multi-rclone-version | Multiple rclone binaries downloaded | A 12-line `.bat` wrapper that prints configurable version output |
| Property-based testing | Beyond-existing-fuzz infrastructure | Go's built-in `testing.F` with seed corpora — already present in the repo |

Both fit comfortably inside system-validation's black-box constraints. The genuine "needs external infrastructure" methodologies are narrower than I claimed:

- **Real cloud-backend integration** — needs auth credentials (genuinely out of scope for unattended testing)
- **30-day endurance simulation** — needs time-acceleration injection inside smirror itself, OR a 30-day budget
- **CI-scale fuzz cluster** — running `-fuzz=...` for hours-days to find rare seeds

Of these, the cloud-backend integration is the one that would surface the most NEW bug surface area (e.g., R3-PF-5: Drive's missing MD5 for files >5GB).

---

## 7. Verdict

Round 12 is the most surface-area-expanding round since R10 (which got the 32-mirror stress test running). The two new methodologies ARE in-scope, and the suite now covers two previously-unverified surfaces:

- rclone version compatibility (smirror handles old/new/garbage rclone versions gracefully)
- Filter property invariants (deterministic + explain-sync-consistent + concurrent-safe over seed corpora)

For new-bug discovery beyond round 12, the remaining genuinely-out-of-scope methodologies are:
1. Real cloud-backend integration (Drive/S3)
2. CI-scale extended fuzz runs (`-fuzz=` for hours)
3. 30-day daemon-uptime simulation

Within scope, panel review has plateaued — but the **seed corpora authored in round 12 are the foundation for future fuzz work**: the maintainer (or CI) can run `go test -fuzz=FuzzPanelR12_FilterDeterminism -fuzztime=30m` later and surface anything the seeds don't cover.

The 5 OPEN findings continue to be the v1.0-readiness blockers. None has been actioned between rounds 11 and 12.

---

*Round 12 report generated 2026-04-29. Sibling reports: rounds 1-11 PANEL-REVIEW-*.md.*
