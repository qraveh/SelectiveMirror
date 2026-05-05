# Release screenshots

Per-release visual evidence: the WiX MSI installer dialogs, the
SmartScreen first-run prompt (until SignPath signing lands), and any
optional screens worth preserving. The mandatory image per release is
`install-telemetry-dialog.png` — the `TelemetryTierDlg` from
`installer/TelemetryConsent.wxi` showing the three opt-in radio
buttons with `None` as default.

## Layout

```
screenshots/
  README.md                          (this file)
  v1.0.0/
    install-telemetry-dialog.png     (mandatory)
    install-license-dialog.png       (optional)
    install-installdir-dialog.png    (optional)
    smartscreen-warning.png          (optional, while unsigned)
  v1.0.1/
    install-telemetry-dialog.png
    ...
```

## Conventions

- One subdirectory per released tag (`v<version>/`), no exceptions.
- File names: lowercase-kebab-case, `.png`.
- PNG preferred (lossless, renders in GitHub's web UI).
- Native resolution; installer dialogs are typically ≤ 200 KB each.

## What this directory is NOT

- Not automated UI testing. Capture is manual on a clean Windows
  install; automation (WinAppDriver / PSWindowsAutomation) is a
  future improvement.
- Not for arbitrary release assets — those live on the GitHub
  Release page (MSI, ZIP, checksums.txt, attestations).
- Not for marketing content — use `docs/` for that.

The release-day capture procedure (when, how, gating relationship to
the GitHub release Publish click) is documented in
[docs/operations/release-day.md](../docs/operations/release-day.md).
