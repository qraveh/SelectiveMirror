# Release runbook — pre-tag procedure

**Audience**: maintainer (Raveh) and the [SM-keeper agent](../../.claude/agents/sm-keeper.md). PR-PM3 (panel review pre-release 2026-04-28).
**Companion**: [release-day.md](release-day.md) covers T-0 through T+24h after the tag is pushed; this file covers everything before the push.

The aim is that "tag, then watch CI" is the only thing left to do at tag time — every quality gate is closed before the tag exists.

---

## 0. Decide whether to release

Default cadence is "tag when there's a coherent batch worth pinning a version to" — usually after a panel review wraps, after a security batch closes, or when telemetry / external users need a specific fix.

Skip a release if:
- Any **High** finding from the most recent panel review is open AND the audience for the next release includes anyone who would hit it. v0.9.x audience is "maintainer + small group of testers" per [README.md](../../README.md#audience-and-maturity-v09x); a hard-to-trigger High can ship under that audience with a CHANGELOG known-issues entry. A public-facing release cannot.
- The merge bar is dirty: untracked panel-review artifacts, uncommitted `system-validation/` tests, or a CHANGELOG `[Unreleased]` that doesn't reflect the diff.
- Live telemetry or `report-bug` traffic shows a recurring signature on the CURRENT released version that hasn't been investigated. Don't bury it under a new release; close it first.

Decision aid: [release-maturity.md](../release-maturity.md) — if any indicator is **red**, default to "no release" until that indicator is at least **yellow** with a documented justification.

---

## 1. Pick the next version

Convention (from CLAUDE.md "Versioning"): tag on minor version bumps OR a coherent patch batch worth a version. Patch number is incremented per commit during dev (`0.9.X-dev`); the X you tag at is the X that the most recent dev commit landed on.

**The invariant the release pipeline asserts (PR-R2)**:

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

Each section in `[Unreleased]` (now `[X.Y.Z]`) should — at the top — carry a `### Known issues` block listing every panel-review finding still open against this version. Each entry must include a test name in `system-validation/` that exercises the finding; that test name is what the release pipeline's allowlist tolerates (PR-Q3).

If any test name in `### Known issues` is NOT in `release.yml`'s `$allowed` array, edit either:
- The CHANGELOG (drop the entry — finding is closed; rename the test or add a fix), OR
- `release.yml` (add the test name to `$allowed`, with the CHANGELOG anchor in a comment).

Both must agree, or system-validation gating drift blocks the release.

---

## 4. Verify the worktree is clean

```bash
git status --short
```

Expected: empty. Any untracked or modified file is a release-bar violation:
- Untracked `system-validation/PANEL-REVIEW-*.md` or `panel_findings_*.go` → commit them. The release will reference panel findings; readers cloning at the tag must see the evidence.
- Modified Go source not in any commit → either commit (with a `0.9.X-dev: ...` message and bumping `var version`) or revert.
- Stray `*.out` / `coverage*.txt` / `.tmp` → `.gitignore` covers these; if they show up, the gitignore rule is wrong.

---

## 5. Run the release dry-run

```bash
gh workflow run release-dryrun.yml \
    -f intended-tag=v0.9.X \
    -f ref=master
```

The dry-run runs the FULL release pipeline (vet + unit + system-validation + report-bug PII smoke + GoReleaser snapshot + MSI build + smoke test) on a fresh CI runner, without uploading anything to a real tag. PR-R1 (panel review pre-release 2026-04-28).

Wait for the workflow to complete (`gh run watch` or the GitHub UI). Required outcome: **all green**. If anything is red:
- A unit-test failure → fix, commit, restart at §4.
- A system-validation failure outside the allowlist → either fix, or extend the allowlist with a CHANGELOG `### Known issues` entry. Don't push the tag until the allowlist matches reality.
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

`release.yml` does NOT depend on `sla-smoke.yml` (CI runners are noisy enough that a transient SLA red would block tagging unnecessarily). But sustained red is a release-readiness signal and the SM-keeper agent treats it as a yellow indicator.

---

## 7. Push the tag

```bash
git tag -a v0.9.X -m "v0.9.X — <one-line summary>"
git push origin v0.9.X
```

The signed-tag form (`git tag -s`) is fine but not required; the GitHub-side build-provenance attestation (PR-S4) is the canonical signature on the artifact. Annotated tags are preferred over lightweight tags so the message is preserved.

DO NOT push tags from a worktree that is ahead of `origin/master`. The release pipeline checks out the tagged commit; if that commit isn't on `origin/master` yet, the tag exists but no one can `git fetch` to it. Push your branch first, confirm the SHA, then tag.

---

## 8. Hand off to release-day playbook

Once the tag is pushed, the release pipeline takes over. Switch to [release-day.md](release-day.md) for T-0 (verify the GitHub release was created), T+10m (MSI uploaded, attestations attached, winget manifest artifact present), T+1h (selfupdate path works for one synthetic user), and T+24h (telemetry health, dashboard sanity).

---

## Appendix A — What changed in this runbook vs. the v0.9.x history

Before the panel review pre-release 2026-04-28, the procedure was:
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
- Supply-chain compromise of CI itself. (Mitigation: GitHub-side attestations PR-S4; user verification command in README.)

These are out of scope for the pre-tag runbook by design. They're tracked in [release-maturity.md](../release-maturity.md) under "Known unknowns".
