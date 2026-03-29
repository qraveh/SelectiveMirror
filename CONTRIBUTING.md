# Contributing to SelectiveMirror

Thank you for your interest in contributing to SelectiveMirror.

## Prerequisites

- **Go 1.26+** -- `winget install GoLang.Go`
- **rclone v1.73+** -- `winget install Rclone.Rclone`
- **Git** -- `winget install Git.Git`

## Building

```bash
git clone https://github.com/qraveh/SelectiveMirror.git
cd SelectiveMirror
go build -o bin/smirror.exe ./cmd/smirror/
```

## Testing

```bash
# Unit tests (139 tests across 10 packages)
go test ./internal/... -v

# Race detector (CGo-free packages)
go test -race ./internal/config/ ./internal/filter/ ./internal/lock/ ./internal/metrics/ ./internal/logging/

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
  notify/                Desktop notifications
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

## License

By contributing, you agree that your contributions will be licensed under the MIT License.
