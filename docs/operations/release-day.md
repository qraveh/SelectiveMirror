# Release-day playbook

**Audience**: maintainer (Raveh) and the [SM-keeper agent](../../.claude/agents/sm-keeper.md). PR-PM3 (panel review pre-release 2026-04-28).
**Companion**: [release-runbook.md](release-runbook.md) ends when the tag is pushed; this file picks up there.

The release pipeline is autonomous after tag-push. This playbook describes what to LOOK AT and WHEN, plus what to do if any of it goes wrong.

---

## T-0 — Tag pushed

What's happening: `release.yml` and the dependent `msi` job are running on `windows-latest` runners.

Where to look:
- `gh run watch` (or `gh run list --workflow=release.yml --limit=3`)

What you're checking:
- The `release` job's first three steps (`Extract version from tag`, `Assert tag matches source version`, `go vet`) all complete in under a minute. If `Assert tag matches source version` fails, the tag is on a commit whose `var version` doesn't match — the runbook §1 invariant. Solution: delete the tag, fix `var version`, re-tag.
- Unit + system-validation tests pass within ~5 minutes. If they fail HERE but passed in the dry-run, something specific to the tag-push runner is broken (Go toolchain, secret access, environment). Re-run; if still red, investigate before manually publishing.

**Hard stop signals at T-0**:
- `report-bug PII smoke` step fails. The release will not produce a draft. Fix the redactor first.
- `Assert tag matches source version` fails. Delete the tag, fix the source, re-tag.

---

## T+10m — Draft release exists, MSI building

What's happening: `release` job has finished GoReleaser; a draft GitHub Release exists with the ZIP attached. The `msi` job has started — it's downloading the smirror.exe artifact, building the MSI, running `installer/smoke-test.ps1`, and uploading the MSI.

Where to look:
- The draft release: `gh release view v0.9.X` (will say "Draft" until both jobs are done and your manual publish click).
- The msi job's "MSI smoke test" step output. SEC-C2 invariants are enforced here; a regression (e.g., a service silently registered, HKCU registry entries, leftover files after uninstall) blocks the upload.

What you're checking:
- The "MSI smoke test" step prints `ALL CHECKS PASSED`.
- The "Verify published MSI matches local" step prints `OK: published MSI matches local <hash>`. PR-Q5 (panel review pre-release 2026-04-28) — confirms GitHub didn't corrupt or substitute the MSI between upload and CDN propagation.

**Hard stop signals at T+10m**:
- MSI smoke fails on a SEC-C2 invariant. Don't publish the draft. Investigate `installer/smoke-test.ps1` output; almost always `Package.wxs` or a recent dotnet/wix change.
- Published-MSI hash mismatch. GitHub upload bit-rot or a CI-side substitution. Re-run the `msi` job; if it persists, do not publish.

---

## T+15m — Set release body and (optionally) publish

What's happening: the `release` job's final step ran `scripts/extract-changelog.ps1` and `gh release edit --notes-file release-notes.md`. The release body now carries the hand-written CHANGELOG section (PR-W4). The release is still in **Draft** state.

Where to look:
- Open the draft release in the browser: `gh release view v0.9.X --web`.
- Read the body. It should be the corresponding `## [X.Y.Z]` section from CHANGELOG.md, with a one-line provenance note at the top.

What you're checking:
- Body looks right. Known-issues section visible to readers. Compatibility/rollback note present (PR-W2 surfaces via the README link).
- Both assets are attached: `SelectiveMirror.msi` and `SelectiveMirror_<version>_windows_amd64.zip`.
- Build-provenance attestations are visible (`gh attestation list <artifact>`).

If everything is right: click **Publish release** (the workflow leaves it as draft on purpose; this is the human go/no-go).

If anything is wrong: edit the body inline (`gh release edit v0.9.X --notes "..."`) before publishing. If the assets are wrong, delete the release (`gh release delete v0.9.X --yes`) and the tag (`git push origin :v0.9.X`), then iterate.

---

## T+30m — winget manifest artifact

What's happening: the `msi` job's `Generate winget manifest` step has produced `winget-manifest-generated.yaml` as a workflow artifact. If repository variable `WINGET_SUBMIT_ENABLED=1` AND secret `WINGET_SUBMIT_PAT` is configured, `wingetcreate update --submit` has already PR'd `microsoft/winget-pkgs`.

Where to look:
- Workflow artifacts: `gh run view <run-id> --log` (or the GitHub UI workflow page).
- microsoft/winget-pkgs PRs by you: https://github.com/microsoft/winget-pkgs/pulls?q=author%3A<your-github-handle>

What you're checking:
- Manifest YAML matches the new version, has the correct InstallerUrl + InstallerSha256.
- If submission was attempted, the PR exists and is queued for the winget-pkgs CI / human review.

If submission was NOT attempted (variable / secret absent), download the artifact and submit manually:

```bash
gh run download <run-id> -n winget-manifest-X.Y.Z
wingetcreate submit -t <pat> winget-manifest-generated.yaml
```

---

## T+1h — Synthetic install + selfupdate

What's happening: nothing automatic. This is your call. You can skip if the release has small audience and the dry-run was thorough.

Where to look:
- A clean Windows VM or a workspace where you can `winget install` without affecting your dev environment.

What to check:
1. Download the MSI from the release URL. Run `certutil -hashfile SelectiveMirror.msi SHA256` and compare to `checksums.txt`.
2. Run `gh attestation verify SelectiveMirror.msi --repo qraveh/SelectiveMirror`. Should print one verified attestation (PR-S4). (CI now runs this same command in-pipeline — see PR-PRE-M4 — but a tester re-run from the user's network is still meaningful.)
3. Click through SmartScreen warning. Install completes.
4. **PR-PRE-D2 (pre-release status panel 2026-05-03): MSI consent dialog visual capture.** During the install wizard, when the "Telemetry preference" dialog (`TelemetryTierDlg` from `installer/TelemetryConsent.wxi`) appears, take a screenshot. Commit it to `screenshots/v<version>/install-telemetry-dialog.png` along with the rest of the release-day evidence. Anchors maturity-dashboard row 3 (MSI consent UI 🟡 → confirms the dialog actually rendered for THIS release; without the artifact the row stays informally 🟡 forever).
5. `smirror version` reports the right version.
6. `smirror selfupdate --check` notices no newer version (since this IS the newest).
7. From a previously-installed older version on a different machine: `smirror selfupdate` actually picks up the new release.

Each is a release-day signal. None being green is a release-quality concern; not all of them being green is normal under "small audience" mode. Step 4 (screenshot) is the only one that creates a tracked artifact in the repo.

---

## T+24h — Telemetry and rollup health

What's happening: telemetry data from this version starts arriving at the Cloudflare Worker → Supabase pipeline. `pg_cron` rollups process it nightly. Per [docs/operations/telemetry-ops.md](telemetry-ops.md), the weekly digest will surface anomalies; the 24h check is a pre-digest sanity glance.

Where to look:
- Cloudflare Worker dashboard (real-time error rate, p99).
- Supabase SQL Editor:

```sql
-- Did this version submit anything yet?
SELECT client_version, COUNT(*) AS envelope_count, MIN(received_at), MAX(received_at)
FROM telemetry.ingest_envelope
WHERE client_version = '0.9.X'
GROUP BY client_version;

-- Anything looking like a recurring signature on the new version?
SELECT bug_kind, COUNT(*) AS n
FROM telemetry.bug_daily_rollup
WHERE rollup_date >= now() - INTERVAL '24 hours'
  AND client_version = '0.9.X'
GROUP BY bug_kind ORDER BY n DESC;
```

What you're checking:
- Envelope count is non-zero (telemetry is reaching the backend at all).
- No recurring signature has fired more than ~3 times. A fresh release with the same bug clustering on day 1 is a regression signal.

**If the envelope count is zero at T+24h AND telemetry is supposed to be enabled** (RELEASE_ALLOW_NO_TELEMETRY_KEY was NOT set, MSI consent dialog defaulted users into a non-`none` tier) — investigate. Check Cloudflare Worker logs for ingestion-side errors, then the binary's own derived-key header. Since smirror does not retry forever, an early-window outage means lost data, not delayed data.

**If a recurring signature appears**:
- See [telemetry-ops.md § Incident: a bad version reported in the wild](telemetry-ops.md#incident-a-bad-version-reported-in-the-wild) for blocklist / patch-and-ship guidance.
- For a Critical regression, the typical answer is "ship a 0.9.(X+1) with the fix and let upgrade events resolve it" — GAP-7 means you cannot ask users to downgrade.

---

## T+7d — Weekly digest

What's happening: the `telemetry-digest.yml` workflow opens a PR with `docs/telemetry/weekly-YYYY-WWNN.md`. This is the post-release follow-up — the action prompts will tell you whether the new version is behaving.

This is no longer release-day work; it's the steady-state cadence covered by [telemetry-ops.md](telemetry-ops.md).

---

## Quick reference — what to do if X breaks

| Symptom | Where to look | What to do |
|---------|---------------|------------|
| `Assert tag matches source version` fails | release.yml step output | Delete tag, fix `var version` in `cmd/smirror/main.go`, re-tag. |
| Unit test fails at release time but passed in dry-run | release.yml step output, compare to dry-run | Investigate flakiness or runner-specific config; re-run; if persistent, escalate. |
| `report-bug PII smoke` fails | release.yml step output | Do not publish. Fix `internal/telemetry/sanitize.go` or `cmd/smirror/cmdreportbug.go`. |
| MSI smoke test fails on SEC-C2 invariant | msi job step output | Investigate `Package.wxs` / wix toolchain change; common cause of v0.9.22-class burns. |
| Published MSI hash mismatch | msi job step output | Re-run `msi` job; if persistent, do not publish — investigate GitHub or CI integrity. |
| Release body is empty / has wrong content | `gh release view <tag>` body | `gh release edit <tag> --notes-file <fixed.md>`. Investigate `scripts/extract-changelog.ps1` against the CHANGELOG. |
| winget submission failed | msi job step output | Download workflow artifact; submit manually with `wingetcreate submit -t <pat> <file>`. |
| Telemetry envelope count zero at T+24h | Supabase ingest_envelope table | Check Cloudflare Worker logs; check binary derived-key header; verify HMAC master rotation didn't invalidate this binary's key. |
| Recurring signature fires on new version | telemetry weekly views | Patch + ship next version (no downgrade path per GAP-7); see telemetry-ops.md "Incident". |

---

## Tag-rollback / re-tag procedure (PR-PRE-D1)

When a tag-day pipeline step fails after the tag was pushed but before you've clicked Publish on the draft release, the recovery path is **delete-and-re-tag** (not amend, not force-update). The release.yml workflow leaves the GitHub Release as Draft until human approval, so this rollback never affects published bytes — but it does need to clean up four artifacts: the git tag (locally + remote), the draft GitHub Release, any partially-uploaded build-provenance attestations, and (if applicable) the winget submission PR.

**When to invoke this procedure**:

- `Assert tag matches source version` fails on release.yml (Failure A) — most common; the version-bump commit didn't land cleanly.
- `gh attestation verify` roundtrip fails on the just-uploaded MSI (Failure B) — OIDC issuer hiccup or `attest-build-provenance` regression.
- `Verify published MSI matches local` (PR-Q5) catches a GitHub upload race (Failure C) — published hash ≠ local hash.
- MSI smoke trips on a runner-image change (Failure D) — windows-latest image churn (WiX 6 vs 7 has bitten before).
- HMAC build-key fingerprint reads `none` or `invalid` (Failure E) — `SMIRROR_TELEMETRY_MASTER_KEY` secret missing or rotated.

**Procedure**:

1. **Confirm the draft release is unpublished.** `gh release view v<X.Y.Z>` — the body should say "Draft". If it's already published, this procedure does NOT apply (selfupdate users would re-download); ship a v<X.Y.Z+1> patch instead.
2. **Delete the draft release.** `gh release delete v<X.Y.Z> --yes`. This also removes uploaded assets (MSI, ZIP, checksums.txt).
3. **Delete provenance attestations** (if any made it through). `gh attestation list <local-msi-path> --repo qraveh/SelectiveMirror` to enumerate; delete via the GitHub UI (Actions → Attestations) — there is no `gh attestation delete` subcommand as of 2026-05.
4. **Delete the tag.** `git push --delete origin v<X.Y.Z>` then `git tag -d v<X.Y.Z>` locally. Order matters: remote first so a stale local tag can't be re-pushed by accident.
5. **(If Failure A)** Fix `var version` in `cmd/smirror/main.go`, commit (`<X.Y.Z>: release-fix: <one-line reason>`), push.
6. **(If Failure D)** Pin the runner image in `.github/workflows/release.yml` (`runs-on: windows-2022` instead of `windows-latest`) — this is the most defensive single edit; keep until the underlying churn is understood.
7. **Re-run release-dryrun.yml** with `intended-tag=v<X.Y.Z> -f ref=master` and confirm green before re-tagging.
8. **Re-tag and re-push.** `git tag -a v<X.Y.Z> -m "..."` + `git push origin v<X.Y.Z>`.
9. **Watch the new release.yml run** via sm-keeper Mode B until both `release` and `msi` jobs are green and the draft body matches CHANGELOG `[X.Y.Z]`.
10. **(If a winget PR was opened against the failed tag)** close it with a comment pointing at the new tag. The next successful release auto-submits a fresh PR if `WINGET_SUBMIT_ENABLED=1`.

**Why delete-and-re-tag rather than force-update**: a force-pushed tag preserves the original tag-creation timestamp and confuses `selfupdate`'s comparison logic; provenance attestations bind to the original SHA and don't migrate. Clean delete + re-create gives sm-keeper Mode B a single chronology to monitor.

**What this procedure does NOT cover** (escalate before acting):

- Tag is already published (`Draft: false`). Selfupdate users have started downloading. Ship a patch release; don't try to un-publish.
- The winget-pkgs PR has merged. Microsoft's manifest store has accepted the failed-tag's bytes. Ship a patch release; don't try to un-merge.
- Provenance attestations have been consumed by a downstream verifier (e.g., a customer's policy engine). Same answer: patch forward.

**Coverage gap acknowledgment**: the procedure above has been documented but has never been exercised against a real failed tag. v1.0.0 is the first tag against this re-tag-aware infrastructure. If any step fails or has stale instructions, edit this file in the same hotfix commit and surface the gap to sm-keeper for the next-release retro.

---

## Appendix — Why no automatic publish

The release-pipeline jobs leave the GitHub Release as **Draft**. Final publication is a human click. This is deliberate:

- Draft state lets you read the release body and see all assets before users do.
- A typo in the CHANGELOG section, a missing artifact, or a wrong attestation ID surfaces here, with one click of recoverability (`gh release delete` is reversible up to that point).
- Once published, users with `smirror selfupdate --include-rclone` will pull within 24 hours.

If you trust your runbook completely, you can flip `release.draft: true` in `.goreleaser.yaml` to `false` for auto-publish. The panel review concluded this is not yet warranted: the cost of a bad release is high, the cost of one human click is low.
