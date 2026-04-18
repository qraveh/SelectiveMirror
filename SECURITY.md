# Security Policy

## Supported Versions

| Version | Supported |
|---------|-----------|
| 0.8.x   | Yes       |
| < 0.8   | No        |

## Reporting a Vulnerability

If you discover a security vulnerability in SelectiveMirror, please report it responsibly:

1. **Do not** open a public GitHub issue.
2. Email **raveh@qodeh.com** with:
   - Description of the vulnerability
   - Steps to reproduce
   - Potential impact
3. You will receive a response within 72 hours.

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
2. **Shell-metacharacter rejection (SEC-C5)**. Before spawning a hook, smirror rejects any environment value (`SMIRROR_PROJECT`, `SMIRROR_FILE`, `SMIRROR_REMOTE`, `SMIRROR_EVENT`) containing `& | < > " ^ $ \` ( ) ;` or control characters. These characters in a filename — e.g., `a&calc.exe` on Windows — would be interpreted as shell operators if the hook script references the variable. Rejection is logged and the hook is skipped for that specific event.
3. **Always quote variables** in hook scripts anyway. The metachar filter is defense-in-depth; writing `echo "%SMIRROR_FILE%"` (Windows) or `echo "$SMIRROR_FILE"` (Unix) is still good practice.
4. **Avoid hooks in Service mode** unless necessary. The LocalSystem account has unrestricted access to the machine.

**Safe hook examples:**
```yaml
# PowerShell (Windows) — note the quoting
post_sync_hook: 'powershell -NoProfile -Command "Write-Host \"Synced: $env:SMIRROR_FILE\""'

# Bash (Unix) — note the double quotes
post_sync_hook: 'echo "Synced: \"${SMIRROR_FILE}\""'
```
