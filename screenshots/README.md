# Release-day screenshots

PR-PRE-D2 (pre-release status panel 2026-05-03). This directory carries
human-captured visual evidence from the release-day playbook (T+1h
synthetic install — see [docs/operations/release-day.md](../docs/operations/release-day.md#t1h--synthetic-install--selfupdate)).

Without these, the maturity dashboard row "MSI consent UI" stays
informally 🟡 forever — there is no other artifact proving the
WiX `TelemetryTierDlg` actually rendered for the released MSI on a
real Windows install. Each release commits its evidence here.

## Layout

```
screenshots/
  README.md                          (this file)
  v1.0.0/
    install-telemetry-dialog.png     (mandatory — closes maturity row 3 for v1.0.0)
    install-license-dialog.png       (optional)
    install-installdir-dialog.png    (optional)
    smartscreen-warning.png          (optional, until SignPath cert lands)
  v1.0.1/
    install-telemetry-dialog.png
    ...
```

## Conventions

- One subdirectory per released tag (`v<version>/`), no exceptions.
- File names are descriptive lowercase-kebab-case ending in `.png`.
- PNG is preferred (lossless, widely viewable in GitHub's web UI).
- Capture at native resolution; do NOT shrink for git size — installer
  dialogs are typically ≤ 200 KB each.
- The mandatory image is `install-telemetry-dialog.png` — the
  `TelemetryTierDlg` showing all three radio options and the default
  selection (`None`).
- Optional images document anything else the maintainer wants future
  readers to see (banner artwork, custom dialog work, SmartScreen
  warning state for unsigned releases).

## What this directory is NOT

- Not a substitute for automated UI testing. WinAppDriver / PSWindowsAutomation
  could close this loop programmatically; deferred as out-of-scope for v1.0.x.
- Not a place to dump arbitrary release artifacts (those go to GitHub
  release assets — MSI, ZIP, checksums.txt, attestations).
- Not for marketing content (use `docs/` or a separate site).

## Workflow integration

The release-day playbook's T+1h step requires committing the screenshot
**before** clicking "Publish release" in the GitHub UI. The maturity
dashboard refresh (SM-keeper Mode C) checks for the presence of
`screenshots/v<latest-tag>/install-telemetry-dialog.png` when computing
the MSI-consent-UI row's color: present and dated within 24h of the
tag → 🟢; absent or stale → 🟡; failure to capture (dialog didn't
render) → 🔴.
