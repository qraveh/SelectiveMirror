# Security Policy

## Supported Versions

| Version | Supported |
|---------|-----------|
| 1.0.x   | Yes       |
| 0.9.x   | Yes (best-effort backports for security-critical fixes only) |
| < 0.9   | No        |

## Reporting a Vulnerability

If you discover a security vulnerability in SelectiveMirror, please report it responsibly:

1. **Do not** open a public GitHub issue.
2. Email **smirror@qodeh.com** with:
   - Description of the vulnerability
   - Steps to reproduce
   - Potential impact
3. The maintainer aims for a first response within 7 days. SelectiveMirror is a single-developer project; please size your expectation accordingly. If a vulnerability is being actively exploited and you receive no response within 7 days, opening a public issue marked "URGENT — security" is appropriate.

## Scope

SelectiveMirror handles file paths, rclone credentials (indirectly via rclone's own config), and local file content. Security-relevant areas include:

- **File access**: smirror reads files within configured mirror directories only.
- **rclone subprocess**: smirror invokes rclone as a child process. rclone credentials are managed by rclone's own config file, never by smirror.
- **SQLite state DB**: contains file paths, hashes, and sync timestamps. No credentials.
- **Log file**: may contain file paths. No credentials.
- **Windows Service**: runs as LocalSystem when installed as a service. The `service install` command auto-resolves paths to avoid CWD-dependent behavior.

## Design Principles

- smirror never stores, transmits, or logs credentials.
- rclone config files are referenced by path, never read or modified by smirror (except to pass `--config` to rclone).
- The single-instance lock prevents concurrent access to the state DB.
- No network listeners: smirror makes outbound connections only (via rclone subprocesses).
- Log, status, and anomaly files are created with mode 0600 (owner-only access).
- Webhook payloads sanitize absolute paths before transmission.
- The MSI installer is perMachine (`%ProgramFiles%\SelectiveMirror\`) — the binary directory is admin-owned, so a standard user cannot replace `smirror.exe` to hijack a running service (SEC-C2).
- Background registration is an opt-in post-install step (`smirror task install` or `smirror service install`), not an automatic MSI side effect — users choose their privilege model.

## Code Signing

**Current status (v1.0.0 onward)**: `smirror.exe` and `SelectiveMirror.msi` ship unsigned pending SignPath Foundation issuance — see CHANGELOG `[1.0.0]` "Bugs known at tag" for the open remediation track and the "Plan" subsection below. SmartScreen will warn on first download from a GitHub release ("Microsoft Defender SmartScreen prevented an unrecognized app from starting"). Click "More info" → "Run anyway" to proceed. Once the winget submission lands, `winget install RavehNeeman.SelectiveMirror` will be available; winget has its own trust chain and bypasses SmartScreen for verified manifests.

**Integrity verification today**: `selfupdate` and manual downloads can be verified against `checksums.txt` (SHA-256) published with each release. As of v0.9.27 the release pipeline also produces [GitHub build-provenance attestations](https://docs.github.com/en/actions/security-guides/using-artifact-attestations-to-establish-provenance-for-builds), verifiable with `gh attestation verify <artifact> --repo qraveh/SelectiveMirror`. Provenance proves the binary was built by this repository's CI on the tagged commit; checksum verification proves the bytes you have are the bytes CI uploaded. Neither is a substitute for an Authenticode signature, but together they cover the supply-chain attack surface that a single hash file does not (compromised release pipeline taints the checksum file alongside the binaries; the GitHub-side attestation is signed by the GitHub Actions OIDC issuer, which a release-pipeline compromise cannot forge).

**Plan (v1.0.6x cycle)**: SignPath Foundation declined the v1.0.0 application on 2026-05-21 (reputation grounds — insufficient stars / external references / community-engagement evidence; not a quality judgment). Microsoft Trusted Signing was the originally-planned fallback but is **unavailable for Israeli individual subscribers** as of 2026-05 (organizations limited to USA / Canada / EU / UK with ≥3-year verifiable history; individuals limited to USA / Canada with onboarding paused). Revised plan in priority order:

1. **OSSign** — free OSS code-signing program (`ossign.org`), eligibility criteria unverified, low cost to try.
2. **SSL.com OV** — commercial primary (~$249/year, individual or organization), validates via business registration + phone callback, supports cloud-CI signing via SSL.com's cloud HSM (no hardware token).
3. **Sectigo OV** — commercial alternative (~$200/year), similar profile.
4. **Certum Open Source** — last resort (~€30/year + USB token; breaks all-cloud CI because signing requires the hardware token to be inserted on a real machine, pushing the signing step out of GitHub Actions into a manual local-machine step).
5. **Reapply to SignPath Foundation in 6-12 months** — zero cost, contingent on reputation accumulating.

Self-signed certificates are explicitly rejected as a path — they don't help SmartScreen and confuse users into thinking a binary is verified when it isn't.

**Defender ML mitigations in place pending signing** (v1.0.60+): PE VERSIONINFO embedding (R-24, `cmd/smirror/versioninfo.json`) and unstripped symbol table (R-25, no `-s -w` in ldflags) — these reduce the Wacatac.B!ml false-positive rate at the local-ML layer but do **not** close the gap entirely. The v1.0.59 MSI was withdrawn pre-publication after Defender flagged it; v1.0.60 still triggers cloud Defender on first download for some users (a per-release WDSI submission re-classifies the specific SHA within 24-72h). A real Authenticode signature is the only durable fix.

## Hook Security (pre_sync_hook / post_sync_hook)

Hooks are user-provided shell commands executed by smirror before/after each file sync. They follow the same trust model as git hooks or CI scripts:

**Execution model:**
- Windows: `cmd.exe /C <hook_command>`
- Unix: `sh -c <hook_command>`
- Timeout: 30 seconds (configurable)

**Privilege level:**
- Foreground mode (`smirror start`): runs as the current user.
- Scheduled Task mode (`smirror task install`): runs as the current user.
- Windows Service mode (`smirror service install`): runs as **LocalSystem** (full system access; requires admin-owned config per SEC-C5).

**Environment variables passed to hooks:**
- `SMIRROR_PROJECT` — mirror name
- `SMIRROR_FILE` — relative file path (may contain shell metacharacters from filenames)
- `SMIRROR_REMOTE` — rclone remote path
- `SMIRROR_EVENT` — `pre_sync` or `post_sync`

**Security requirements (enforced by smirror):**
1. **Admin-owned config when running as Service (SEC-C5, service-wide as of 0.8.51-dev)**. If smirror is installed as a Windows Service, it refuses to start unless the config file is owned by Administrators or LocalSystem. This is enforced at `smirror service install` time and again at service startup. The requirement was originally scoped to hook-bearing configs only; it was widened because `rclone_path` / `rclone_extra_flags` / delete-policy / filter rules also give a non-admin config-writer arbitrary-code-execution-as-SYSTEM. Remedy: move config to an admin-writable-only location such as `%ProgramData%\SelectiveMirror\config.yaml`, or use per-user task mode instead (`smirror task install` — no admin required, no SYSTEM).
2. **Shell-metacharacter rejection (SEC-C5)**. Before spawning a hook, smirror rejects any environment value (`SMIRROR_PROJECT`, `SMIRROR_FILE`, `SMIRROR_REMOTE`, `SMIRROR_EVENT`) containing any of: `& | < > " ^ $ \` ( ) ;` (POSIX shell metacharacters), `% !` (cmd.exe variable expansion + delayed expansion), control characters `0x00–0x1f`, or Unicode RTL-override / left-to-right-override / zero-width-space / zero-width-non-joiner / zero-width-joiner / BOM. These characters in a filename — e.g., `a&calc.exe` on Windows or `file%PATH%.txt` — would be interpreted as shell operators or expanded to environment values if the hook script references the variable. Rejection is logged and the hook is skipped for that specific event.
3. **Always quote variables** in hook scripts anyway. The metachar filter is defense-in-depth; writing `echo "%SMIRROR_FILE%"` (Windows) or `echo "$SMIRROR_FILE"` (Unix) is still good practice.
4. **Avoid hooks in Service mode** unless necessary. The LocalSystem account has unrestricted access to the machine.

**Safe hook examples:**
```yaml
# PowerShell (Windows) — note the quoting
post_sync_hook: 'powershell -NoProfile -Command "Write-Host \"Synced: $env:SMIRROR_FILE\""'

# Bash (Unix) — note the double quotes
post_sync_hook: 'echo "Synced: \"${SMIRROR_FILE}\""'
```
