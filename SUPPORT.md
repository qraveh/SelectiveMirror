# Getting Help

SelectiveMirror is a single-developer open-source project. This document points at the right surface for the kind of question you have.

## Bug reports

Open a GitHub issue using the [bug report template](https://github.com/qraveh/SelectiveMirror/issues/new?template=bug_report.yml). Run `smirror report-bug --stdout` first and paste the redacted output — it carries the version, platform, rclone info, and the last 30 log lines, all sanitized.

## Feature requests

Use the [feature request template](https://github.com/qraveh/SelectiveMirror/issues/new?template=feature_request.yml). Be concrete about the use case; "X but for Y" is more useful than "please add X".

## Security vulnerabilities

**Do not** open a public issue. See [SECURITY.md](SECURITY.md) for the disclosure process — short version: email `smirror@qodeh.com` with description, reproduction, and impact.

## Questions and discussion

GitHub Issues are also fine for "how do I…" or "is this expected behavior?" questions; tag the issue with the `question` label or use the bug-report template's "I'm not sure if this is a bug" framing. There's no SLA on questions; expect best-effort response within a week or two.

## What this project is not

- **Not a commercial product.** No paid support tier exists. Severity-based response priorities don't apply.
- **Not a sponsorship target.** See [`.github/FUNDING.yml`](.github/FUNDING.yml).
- **Not seeking large architectural rewrites from drive-by contributors.** Bug fixes and small improvements are very welcome; please open an issue first for anything beyond ~50 lines so we can scope it together.

## Contribution

See [CONTRIBUTING.md](CONTRIBUTING.md).
