# Contributing to SelectiveMirror

Thank you for your interest in contributing to SelectiveMirror.

## Prerequisites

- **Go 1.26+** -- `winget install GoLang.Go`
- **rclone v1.73+** -- `winget install Rclone.Rclone`
- **Git** -- `winget install Git.Git`
- **MinGW-w64** (C toolchain for CGo; required by the SQLite driver) -- `winget install BrechtSanders.WinLibs.POSIX.UCRT`. After install, ensure `gcc.exe` is on your PATH (the WinLibs installer creates it under `%LOCALAPPDATA%\Microsoft\WinGet\Packages\BrechtSanders.WinLibs.POSIX.UCRT_*\mingw64\bin\`). The built `smirror.exe` statically links SQLite — end users don't need a C compiler.

## Building

```bash
git clone https://github.com/qraveh/SelectiveMirror.git
cd SelectiveMirror
go build -o bin/smirror.exe ./cmd/smirror/
```

## Testing

```bash
# Unit tests (500+ tests across 14 packages)
go test ./internal/... -v

# Race detector (CGo-free packages)
go test -race ./internal/config/ ./internal/filter/ ./internal/logging/ ./internal/lock/ ./internal/metrics/ ./internal/watcher/

# Lint
go vet ./...

# Integration tests (requires rclone configured with a local remote)
powershell -File test/run_tests.ps1
```

All tests must pass before submitting a pull request.

## Project Structure

```
cmd/smirror/             CLI entry point and all commands
internal/
  config/                YAML config loading and validation
  filter/                .syncignore parser (gitignore syntax)
  watcher/               Filesystem monitoring (fsnotify)
  sync/                  rclone invocation and quiescence
  state/                 SQLite state store (WAL mode)
  lock/                  Single-instance file lock
  metrics/               Thread-safe counters and status.json
  logging/               slog + file handler with shared access
  rclone/                rclone binary detection
  service/               Windows Service (SCM integration)
  notify/                Desktop notifications and webhook alerts
  anomaly/               Anomaly classification, recording, rotation
  hooks/                 Pre/post-sync hook execution
  telemetry/             Opt-in anonymous telemetry + update check
installer/               WiX MSI installer source
test/                    Integration test suite
docs/                    User, installation, and developer manuals
```

## Pull Request Process

1. Fork the repository and create a branch from `master`.
2. Make your changes. Keep commits focused and atomic.
3. Add or update tests for any changed behavior.
4. Run `go test ./internal/...` and `go vet ./...` -- all must pass.
5. Update documentation if your change affects user-facing behavior.
6. Submit a pull request with a clear description of the change.

## Code Style

- Follow standard Go conventions (`gofmt`, `go vet`).
- Use `slog` for all logging (not `fmt.Println` or `log`).
- Platform-specific code goes in `_windows.go` / `_unix.go` files.
- Error messages should be lowercase and descriptive.
- Comments on exported functions follow Go doc conventions.

## Versioning

SelectiveMirror follows [semver](https://semver.org/):

- **PATCH**: bug fixes, refactors, renames -- no new user-facing functionality
- **MINOR**: new features, commands, config options, architectural changes
- **MAJOR**: breaking changes to config format, CLI interface, or behavior

Patch numbers increment on each change. Tags are created only for minor releases.

## Reporting Bugs

Use `smirror report-bug --stdout` to generate a diagnostic report, then file an issue using the [bug report template](https://github.com/qraveh/SelectiveMirror/issues/new?template=bug_report.yml).

## Dependency Policy (supply-chain)

All Go dependencies are pinned by cryptographic hash in `go.sum`. CI runs
`go mod verify` on every build to detect tampering with cached module bytes.

**Do not run `go get -u` without deliberate review.** Dependency upgrades require:

1. Reading the upstream changelog/commits between the pinned and target versions
2. For security-critical packages (see list below), an explicit re-audit of
   the new bytes before committing the `go.sum` update
3. Full test suite passing

**Security-critical dependencies** (touched on every file event or handling
untrusted input):

| Package | Role | Last audit |
|---------|------|------------|
| `github.com/git-pkgs/gitignore` | `.syncignore` pattern matching | v1.1.1 — see `docs/security-audit-2026-04-18.md` SEC-C1 |
| `github.com/fsnotify/fsnotify` | filesystem events | (upstream maintained, widely used) |
| `github.com/mattn/go-sqlite3` | state DB (SQLite driver, CGo) | (upstream mature since 2014, battle-tested; upstream SQLite CVEs handled via library updates) |
| `go.yaml.in/yaml/v3` | config parsing | (upstream maintained) |

When upgrading `git-pkgs/gitignore` specifically: audit the diff in
`gitignore.go` and `wildmatch.go` before merging. We only use the bare
`Matcher{}` struct literal plus `AddPatterns`, `Errors`, and `MatchPath` —
do not enable the `New()` filesystem auto-loader without re-evaluating
the attack surface.

## License

By contributing, you agree that your contributions will be licensed under the MIT License.
