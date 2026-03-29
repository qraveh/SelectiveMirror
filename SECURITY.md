# Security Policy

## Supported Versions

| Version | Supported |
|---------|-----------|
| 0.3.x   | Yes       |
| < 0.3   | No        |

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
