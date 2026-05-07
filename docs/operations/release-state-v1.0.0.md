# Release-state record — v1.0.0

A one-time snapshot of the surrounding infrastructure at v1.0.0 tag-day, beyond what the git tag and the GitHub Release page already preserve. Useful as a forensic baseline when comparing later state against this point-in-time.

## Anchors

| Item | Value |
|---|---|
| Tag | `v1.0.0` (annotated) |
| Tag commit | `2a962ecf51c13c9cd03168b64b7bcb00e6693044` |
| Tagger | `Raveh <raveh@qodeh.com>` |
| Tag date | `2026-05-06 11:02:46 +0300` |
| Tag message | `SelectiveMirror v1.0.0 — first stable release` |
| Release URL | https://github.com/qraveh/SelectiveMirror/releases/tag/v1.0.0 |
| Published-at | `2026-05-06T08:29:44Z` |
| Repo visibility flip | concurrent with tag push (within minutes) |
| Source `var version` | `"1.0.0"` (no `-dev` suffix) |
| Telemetry build-key fingerprint | `efcf4fcc` (HMAC of derived key truncated to 8 hex) |

## Release pipeline

| Workflow | Run ID | Conclusion | Wall-time |
|---|---|---|---|
| `release.yml` (release job + msi job) | `25423556468` | ✅ success | 10m20s |
| Final dryrun before tag (last green) | `25384552555` | ✅ success | dryrun-release 6m55s + dryrun-msi 2m05s |

## Release-page assets (preserved by GitHub indefinitely)

| Asset | SHA-256 |
|---|---|
| `SelectiveMirror.msi` | `7c14b50531727e0addba7ccd297ebac69c03d9707baa696b4eb9b1bd6bb5a5a4` |
| `SelectiveMirror_1.0.0_windows_amd64.zip` | `3b4b1cfbbb523ff18da4e3f002f1232d2aed9cdb1af3530d935c012b6852012d` |
| `smirror.exe` (extracted from ZIP) | `1bbd36a24c8df513731b030809119b795bcd3650fd961c79837de0b7d73bc247` |
| `checksums.txt` | covers ZIP only (MSI hash missing — v1.0.1 defect) |

Both attestations (smirror.exe and SelectiveMirror.msi) are stored in GitHub's attestation sidechain. Verification command:

```pwsh
gh attestation verify <artifact> --repo qraveh/SelectiveMirror
```

## Cloudflare Worker — `smirror-telemetry`

| Item | Value |
|---|---|
| Worker URL | `https://smirror-telemetry.selectivemirror.workers.dev` |
| Worker script | `smirror-telemetry` (single Worker on the account) |
| Latest deploy at tag-day | version `e5024ee6-359...`, deployed `2026-05-03T04:08:46Z` |
| Notes | Worker code matches `worker/src/index.ts` at the tag commit. Re-deploy cadence: only on worker/ changes. |

If a Worker rollback to the v1.0.0-day version is ever needed: the deploy ID `e5024ee6-359` can be re-promoted via the Cloudflare dashboard (Workers → smirror-telemetry → Versions). Preserved here so the rollback path is unambiguous.

## Supabase backend

| Item | Value |
|---|---|
| Project ref | `qkspigvkniiiwxggdvbr` (from `worker/wrangler.toml`) |
| Region | West EU (Ireland) |
| Schema source-of-truth | `docs/telemetry-v2.sql` at commit `2a962ec` |
| pg_cron rollup schedule | daily 02:00 + 02:15 UTC (per script docstring) |
| Vault secret | `telemetry_master_key` (used by Worker; rotation = re-issue derived keys) |

Schema baseline is in the repo at the tag. If the live schema ever diverges from `docs/telemetry-v2.sql@v1.0.0`, that's a drift signal worth investigating.

## Initial telemetry signal

First confirmed end-to-end ingest (synthetic, from the maintainer's machine):

| Step | Time (UTC) | Detail |
|---|---|---|
| MSI install (`/qb` + `INSTALL_TELEMETRY_TIER=reliability`) | 2026-05-07T12:38:14Z | log at `C:\Windows\Temp\smirror-install.log` |
| install_id generated | 2026-05-07T12:42:41Z | `sm-fee2d8d6a51cec5c0396407f4fd48064` (after bounce-through-none — see DEFECT below) |
| `first_seen` POST → Worker | 2026-05-07T12:42:43Z | status: success, 1 subrequest to Supabase |
| `first_seen` POST → Worker (retry, second start) | 2026-05-07T12:43:20Z | status: success |

These are the first events on the live pipeline. The k-anonymity floor of 5 in the published digest will continue to suppress single-install signals; the Worker analytics (Cloudflare GraphQL API) is the operator's escape valve for low-N visibility.

## Network constraint (operator note)

The maintainer's primary dev machine egresses through ASN 378 (IUCC — Israel Inter-University Computation Center, AS378) regardless of whether traffic originates over Wi-Fi or Galaxy S10 cellular hotspot. Both paths transit IUCC's edge, where outbound TCP is filtered to standard ports (443, 80, 22, etc.).

Practical implications for any maintainer (current or future) operating from this network:

| Operation | Reachable from dev box? |
|---|---|
| `gh` API operations (HTTPS) | ✅ |
| Worker HTTPS POST / probe | ✅ |
| HTTPS to Supabase REST surface | ✅ (auth required) |
| Direct Postgres (TCP 6543 pgbouncer pooler) | ❌ — egress filtered |
| Direct Postgres (TCP 5432 with `db.<ref>.supabase.co`) | ❌ — Supabase migrated this path to IPv6-only; not reachable from this NAT |
| `wrangler tail` (WebSocket over 443) | ✅ |
| Cloudflare GraphQL API (HTTPS) | ✅ — used above for Worker analytics |

For any DB-side operation:
- Run via `gh workflow run telemetry-digest.yml` (CI runner has unrestricted egress)
- Or query via Cloudflare GraphQL for Worker-side metrics
- Or run the script from a different network (different SIM, café Wi-Fi, etc.)

This is not a regression — it's been the case throughout the v0.9.x development cycle. Documented here so future-maintainer doesn't waste time debugging the same wall.

## Defects discovered at v1.0.0 install-day (file as v1.0.1 work)

### DEFECT-1 (silent telemetry failure under MSI consent dialog)

**Symptom**: User installs MSI, picks `Standard` or `Reliability` at the consent dialog (or via `INSTALL_TELEMETRY_TIER=` property). Registry tier is set correctly. State DB is created. Daemon starts. Logs:

```
WARN msg="install-event submit skipped: install_id is empty (state DB inconsistent?)"
```

`first_seen` event never fires. Telemetry pipeline silently no-ops for the lifetime of that install.

**Root cause**: install_id generation lives in `cmd/smirror/cmd_telemetry.go::cmdTelemetrySet`, gated on a `prev (None) → new (non-None)` transition. The MSI installer's `INSTALL_TELEMETRY_TIER` property writes the registry directly, bypassing the CLI flow. `prev` is read as already non-None on first daemon startup, so the transition logic doesn't fire and install_id stays empty.

**Workaround for affected installs**: bounce through none —
```pwsh
smirror telemetry none
smirror telemetry reliability
```

The `none → reliability` transition correctly generates install_id.

**Fix for v1.0.1**: at daemon startup OR at first install-event submit, if tier ≠ none AND install_id is empty, generate install_id idempotently. Two-line fix in `cmdStart` or `SendInstallEventsIfDue`.

### DEFECT-2 (release-notes mojibake)

**Symptom**: GitHub release notes body shows `â€"` instead of `—` (em-dash) and a leading BOM character.

**Root cause**: `.github/workflows/release.yml` Extract-release-notes step calls `powershell.exe` (PowerShell 5.1) instead of `pwsh` (PowerShell 7+). PS 5.1 `Set-Content -Encoding UTF8` writes UTF-8-WITH-BOM; PS 7+ writes UTF-8-NoBOM. GitHub's API stores the bytes as-is, then renders them as Latin-1.

**Manual fix applied at v1.0.0**: re-uploaded body via `gh release edit --notes-file <local-file>`.

**Fix for v1.0.1**: change `powershell` → `pwsh` in `release.yml:Extract release notes from CHANGELOG`. One-line change.

### DEFECT-3 (MSI hash missing from checksums.txt)

**Symptom**: README directs users to `certutil -hashfile SelectiveMirror.msi SHA256` and compare to `checksums.txt`. The MSI hash is not in `checksums.txt` (only the ZIP hash is).

**Root cause**: GoReleaser only generates checksums for archive outputs (ZIP). The MSI is built later by `installer/build-msi.ps1` in the `msi` job; the workflow never appends its hash to the checksums file.

**Fallback**: build-provenance attestation works for MSI integrity verification independently of the checksum file.

**Fix for v1.0.1**: in the `msi` job, after the MSI is built and before upload, append `<sha256>  SelectiveMirror.msi` to `checksums.txt` and re-upload via `gh release upload v1.0.0 checksums.txt --clobber`.

## Maintenance branch decision

No `release-1.0` (or `v1.0.x`) branch was created at v1.0.0 tag-day. The project has linear-release semantics: v1.0.1 includes everything since v1.0.0. If a future critical bug requires a `v1.0.0.1` patch tag without dragging in v1.0.1 changes, the branch can be created retroactively from the tag:

```pwsh
git checkout -b release-1.0 v1.0.0
git push origin release-1.0
```

Branch creation is not a tag-day requirement.

## Branch protection at v1.0.0 tag-day

Set on master at v1.0.0+ε (~2 hours post-tag, day-of):

| Setting | State |
|---|---|
| `enforce_admins` | true |
| `allow_force_pushes` | false |
| `allow_deletions` | false |
| `required_status_checks` | none (solo direct pushes) |
| `required_pull_request_reviews` | none |
| `required_linear_history` | false |

Configurable via `gh api -X PUT repos/qraveh/SelectiveMirror/branches/master/protection --input <json>`.

## Stale draft cleanup

At v1.0.0 publish, five stale drafts from earlier cycles (v0.9.26, v0.7.0, v0.6.0, v0.5.0, v0.4.0) existed on the Releases page. None were public (drafts are maintainer-only); none referenced live audiences. Deleted at `2026-05-06T~14Z` via `gh release delete <tag> --yes` per draft. The git tags remain in the repo's tag history; only the empty Release-object pages were removed.

## Verification recipes

Quick checks any future maintainer can run to confirm "is this binary really v1.0.0":

```pwsh
# Hash check (only ZIP currently — MSI hash will land in v1.0.1's checksums.txt)
certutil -hashfile SelectiveMirror_1.0.0_windows_amd64.zip SHA256
# expected: 3B4B1CFBBB523FF18DA4E3F002F1232D2AED9CDB1AF3530D935C012B6852012D

# Build-provenance attestation (gold standard)
gh attestation verify SelectiveMirror.msi --repo qraveh/SelectiveMirror
gh attestation verify smirror.exe --repo qraveh/SelectiveMirror
# both should exit 0; subject.digest should match local sha256 of the file

# Once installed, version + telemetry build-key:
& "C:\Program Files\SelectiveMirror\smirror.exe" version
# expected: smirror 1.0.0 / Copyright (c) 2026 Raveh Neeman / telemetry build-key: efcf4fcc

# Anything other than `efcf4fcc` (and not "none") on a 1.0.0 binary
# means it wasn't built by the v1.0.0 release pipeline.
```

If `telemetry build-key: none` is observed on a binary that claims `smirror 1.0.0`, the binary is a local `go build` that was placed at the install path (NOT the released MSI). The release pipeline's ldflag injection produces the `efcf4fcc` value at link time; a direct `go build` never sees that flag.
