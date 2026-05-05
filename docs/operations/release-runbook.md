# Release runbook — pre-tag procedure

**Audience**: maintainer and the [release-day operator workflow](../../.claude/agents/sm-keeper.md).
**Companion**: [release-day.md](release-day.md) covers T-0 through T+24h after the tag is pushed; this file covers everything before the push.

The aim is that "tag, then watch CI" is the only thing left to do at tag time — every quality gate is closed before the tag exists.

---

## 0. Decide whether to release

Default cadence is "tag when there's a coherent batch worth pinning a version to" — usually after a panel review wraps, after a security batch closes, or when telemetry / external users need a specific fix.

Skip a release if:
- Any **High** finding from the most recent review is open AND would affect the audience the release is targeted at (per the audience tiers in [docs/release-maturity.md](../release-maturity.md)). A hard-to-trigger High can ship under a narrow audience with a CHANGELOG known-issues entry; a wide-audience release cannot.
- The merge bar is dirty: untracked panel-review artifacts, uncommitted `system-validation/` tests, or a CHANGELOG `[Unreleased]` that doesn't reflect the diff.
- Live telemetry or `report-bug` traffic shows a recurring signature on the CURRENT released version that hasn't been investigated. Don't bury it under a new release; close it first.

Decision aid: [release-maturity.md](../release-maturity.md) — if any indicator is **red**, default to "no release" until that indicator is at least **yellow** with a documented justification.

---

## 1. Pick the next version

Convention (from CLAUDE.md "Versioning"): tag on minor version bumps OR a coherent patch batch worth a version. Patch number is incremented per commit during dev (`0.9.X-dev`); the X you tag at is the X that the most recent dev commit landed on.

**The invariant the release pipeline asserts**:

```
strip-dev(cmd/smirror/main.go::var version) == tag-without-leading-v
```

So if `var version = "0.9.27-dev"`, the next tag must be `v0.9.27`, not `v0.9.26` (already used) and not `v0.9.28` (jumps the source). To shift the next tag (e.g. you want `v0.10.0` instead of `v0.9.28`), bump `var version` to `0.10.0-dev` in a regular commit BEFORE running the runbook.

---

## 2. Promote CHANGELOG `[Unreleased]` → `[X.Y.Z]`

Open `CHANGELOG.md`. The first heading is `## [Unreleased]`. It should already contain everything that has been merged since the last tag — if it doesn't, fix that first (the per-commit `0.9.X-dev: <msg>` log is a strong hint; the actual prose lives in `[Unreleased]`).

Promotion = rename `[Unreleased]` to `[X.Y.Z] — YYYY-MM-DD` (today's date in the maintainer's locale; `release.yml` doesn't care, the date is for human readers). Then add a new empty `## [Unreleased]` heading immediately above so the next dev cycle has somewhere to accumulate.

Example diff for the v0.9.26 tag:
```diff
-## [Unreleased]
+## [Unreleased]
+
+## [0.9.26] — 2026-04-29
```

Commit on its own with a message like `CHANGELOG: promote [Unreleased] to [0.9.26]`. Do NOT bump `var version` in this commit — the assertion in §1 needs `var version` to still be `0.9.26-dev` so that `strip-dev` matches `0.9.26`.

---

## 3. Make sure the `## Known issues` section is present and accurate

Each section in `[Unreleased]` (now `[X.Y.Z]`) should — at the top — carry a `### Known issues` (or `### Bugs known at tag`) block listing every review finding still open against this version. Each entry must include a test name in `system-validation/` that exercises the finding; that test name is what the release pipeline's allowlist tolerates.

The allowlist lives at `system-validation/allowlist.txt` — a single source of truth shared by `release.yml` and `release-dryrun.yml`. The companion linter `scripts/check-allowlist-vs-changelog.ps1` enforces the agreement automatically:

```bash
powershell -ExecutionPolicy Bypass -File scripts/check-allowlist-vs-changelog.ps1
```

This runs at the top of the system-validation step in BOTH workflows. If a test in the allowlist has no `system-validation/<TestName>` mention in `CHANGELOG.md`, the release fails before tests even start. To resolve drift:

- Drop the entry from `system-validation/allowlist.txt` (the test is no longer tolerated as RED — it passes, was reclassified, or was deleted), OR
- Add a CHANGELOG bullet in `### Known issues` / `### Bugs known at tag` referencing the test name verbatim with rationale.

The asymmetric direction (allowlist ⊆ CHANGELOG mentions) is intentional: CHANGELOG entries describing CLOSED tests still mention the test name as evidence of closure but correctly do NOT appear in the allowlist.

---

## 4. Verify the worktree is clean

```bash
git status --short
```

Expected: empty. Any untracked or modified file is a release-bar violation:
- Untracked validation artifacts (panel-review notes, ad-hoc test scaffolds) → either commit or delete; a release-bar violation. The release will reference review findings; readers cloning at the tag must see whatever evidence is intended to be public.
- Modified Go source not in any commit → either commit (with a `0.9.X-dev: ...` message and bumping `var version`) or revert.
- Stray `*.out` / `coverage*.txt` / `.tmp` → `.gitignore` covers these; if they show up, the gitignore rule is wrong.

---

## 5. Run the release dry-run

```bash
gh workflow run release-dryrun.yml \
    -f intended-tag=v0.9.X \
    -f ref=master
```

The dry-run runs the FULL release pipeline (vet + go-mod-verify + unit + allowlist-vs-CHANGELOG linter + system-validation + report-bug PII smoke + GoReleaser snapshot + build-key fingerprint + MSI build + MSI smoke test) on a fresh CI runner, without uploading anything to a real tag. As of the 2026-05-03 pre-release status review the dry-run carries the same gates as the real release except: no upload, no SLSA attestations, no winget submission, HMAC master key optional.

Wait for the workflow to complete (`gh run watch` or the GitHub UI). Required outcome: **all green**. If anything is red:
- A unit-test failure → fix, commit, restart at §4.
- An allowlist-vs-CHANGELOG drift → fix the file pair per §3 above.
- A system-validation failure outside the allowlist → either fix, or extend the allowlist with a CHANGELOG `### Known issues` entry (in that order — the linter will reject the inverse).
- A build-key fingerprint mismatch (`telemetry build-key: invalid` or unexpected `none`) → check `.goreleaser.yaml` ldflag template + the `SMIRROR_TELEMETRY_DERIVED_KEY` secret/var plumbing.
- An MSI build failure → this is the v0.9.22 tag-burn class of failure; fix locally, recommit, restart.
- A report-bug PII leak → URGENT. Do not release. The redactor is broken; investigate `internal/telemetry/sanitize.go` and `cmd/smirror/cmdreportbug.go` before doing anything else.

---

## 6. (Optional) Confirm SLA smoke is recently green

```bash
gh run list --workflow=sla-smoke.yml --limit=5
```

The newest run should be **success**. If the last green is more than 48 hours old, dispatch one and wait:

```bash
gh workflow run sla-smoke.yml -f ref=master
```

`release.yml` does NOT depend on `sla-smoke.yml` (CI runners are noisy enough that a transient SLA red would block tagging unnecessarily). But sustained red is a release-readiness signal and operators treat sustained red as a yellow indicator.

---

## 7. Push the tag

```bash
git tag -a v0.9.X -m "v0.9.X — <one-line summary>"
git push origin v0.9.X
```

The signed-tag form (`git tag -s`) is fine but not required; the GitHub-side build-provenance attestation is the canonical signature on the artifact. Annotated tags are preferred over lightweight tags so the message is preserved.

DO NOT push tags from a worktree that is ahead of `origin/master`. The release pipeline checks out the tagged commit; if that commit isn't on `origin/master` yet, the tag exists but no one can `git fetch` to it. Push your branch first, confirm the SHA, then tag.

---

## 8. Hand off to release-day playbook

Once the tag is pushed, the release pipeline takes over. Switch to [release-day.md](release-day.md) for T-0 (verify the GitHub release was created), T+10m (MSI uploaded, attestations attached, winget manifest artifact present), T+1h (selfupdate path works for one synthetic user), and T+24h (telemetry health, dashboard sanity).

---

## Appendix A — What changed in this runbook vs. the v0.9.x history

Before the 2026-04-28 pre-release review, the procedure was:
- Promote CHANGELOG.
- Push tag.
- Watch CI.
- Hope MSI build doesn't break.

That worked when MSI build was simple, but the v0.9.22 tag was burned twice because a WiX 6 schema regression was only caught at tag-push time. The dry-run workflow + tag-source assertion + system-validation gate + post-publish hash check together raise the floor: every problem the release pipeline could surface AT tag time is now surfaceable BEFORE.

The cost is roughly 10 minutes of dry-run wall-clock per tag, which is far less than the cost of a single tag-burn cycle.

---

## Appendix B — Failure modes the runbook cannot catch

- A bug in code that all tests miss. (Mitigation: `system-validation` keeps growing; panel review is the human eye.)
- An external dependency change that breaks AT install time on a real user's machine but not on the CI runner (e.g., rclone backend API shift). (Mitigation: SLA smoke catches this within 24h on a real backend; release-day playbook tells you what to look at.)
- Telemetry endpoint outage post-release. (Mitigation: release-day T+1h check; rollback path is "publish next tag with fix".)
- Supply-chain compromise of CI itself. (Mitigation: GitHub-side build-provenance attestations; user verification command in README.)

These are out of scope for the pre-tag runbook by design. They're tracked in [release-maturity.md](../release-maturity.md) under "Known unknowns".
