# WDSI false-positive submission — v1.0.59

**Purpose.** Microsoft Defender flagged the v1.0.59 MSI as
`Trojan:Win32/Wacatac.B!ml` on first download. The `!ml` suffix marks
this as a machine-learning heuristic detection, not a signature match.
This document is a ready-to-paste submission template for the Microsoft
WDSI portal so the FP gets reclassified.

## Where to submit

Microsoft Defender Security Intelligence portal — file submission:

  https://www.microsoft.com/en-us/wdsi/filesubmission

Click **Submit a file for malware analysis**. Sign in with a Microsoft
account (any will work — doesn't have to be the developer's).

## Form fields

| Field | Value |
|---|---|
| **What kind of file are you submitting?** | Software |
| **Detection name** | `Trojan:Win32/Wacatac.B!ml` |
| **Customer impact** | We disagree with the detection (false positive) |
| **Definition update version** | (look at Defender → Virus & threat protection updates → Security intelligence version, paste it here) |
| **Product name** | SelectiveMirror |
| **Product version** | 1.0.59 |
| **Submitter name** | Raveh Neeman |
| **Submitter email** | smirror@qodeh.com |
| **Submitter role** | Software developer (project maintainer) |

## File uploads

Upload TWO files (both via the form, multiple attachments):

1. **`SelectiveMirror.msi`** — the released installer.
   - SHA-256: `3b81b02a9e81efc5695d21a04190788714afc9fd2187b5d7261041e4395bc2bf`
   - Size: 4,419,584 bytes (~4.21 MB)
   - Download: https://github.com/qraveh/SelectiveMirror/releases/download/v1.0.59/SelectiveMirror.msi

2. **`smirror.exe`** — the binary inside the MSI.
   - Extract from the MSI via `msiexec /a SelectiveMirror.msi /qn TARGETDIR=<temp>` or via 7-Zip / lessmsi.
   - The binary is also the entire content of `SelectiveMirror_windows_amd64.zip`.

## Comments / additional details (paste into the form's "additional details" field)

```
SelectiveMirror is an MIT-licensed open-source file-mirroring tool
for Windows. https://github.com/qraveh/SelectiveMirror

The v1.0.59 MSI was built by GitHub Actions CI from public source
code at the tagged commit. Build provenance attestation is available
via:

    gh attestation verify SelectiveMirror.msi --owner qraveh

(exits with code 0 confirming the binary came from this repository's
CI pipeline on the tagged commit; SLSA Provenance v1 envelope.)

Detection details:
- Detection name: Trojan:Win32/Wacatac.B!ml
- File:           SelectiveMirror.msi (4.21 MB)
- SHA-256:        3b81b02a9e81efc5695d21a04190788714afc9fd2187b5d7261041e4395bc2bf
- Build pipeline: GitHub Actions workflow `release.yml`
- Build runner:   ubuntu-latest (Go) + windows-latest (WiX MSI build)
- Tagged commit:  96951361d61f6527622bd37f346493f84f5eb909 (v1.0.59)
- Source repo:    https://github.com/qraveh/SelectiveMirror

The binary is unsigned. Authenticode signing is pending: the project
was applying to SignPath Foundation but the foundation rejected the
application on reputation grounds (small OSS project). A commercial
code-signing certificate is being procured (Azure Trusted Signing).
The next patch release will ship signed; the v1.0.59 release went
unsigned only because the signing pivot is in progress.

The `!ml` machine-learning detection is the first detection from any
AV engine for any released SelectiveMirror binary across the v0.4 →
v1.0.0 → v1.0.59 history. The project has never bundled, dropped,
fetched, executed, or injected any third-party payload. It is a
single Go binary that watches local file changes (via Windows
ReadDirectoryChangesW), filters them through gitignore-style rules,
and invokes the external `rclone` executable (user-provided) to
copy stable files to a user-configured cloud backend.

We believe the detection triggers on unsigned-Go-binary heuristics
the ML model uses for the Wacatac classifier, and request that the
file be reclassified as not-a-threat.

Thank you.
```

## After submission

Microsoft typically responds within 24-72 hours for OSS-project FPs.

When the reclassification lands, Defender cloud-protection clients
will automatically pick up the new verdict (no user re-download
needed). The "Severe — Trojan:Win32/Wacatac.B!ml" entry in users'
Defender Protection History will be re-marked as clean.

Tracking: paste the WDSI submission ID (the form gives one) here for
audit-trail.

```
WDSI submission ID: ____________________________
Submitted at:        ____________________________
First response:      ____________________________
Reclassified at:     ____________________________
```

## If Microsoft confirms the file IS malicious (very unlikely)

Stop the release immediately. The provenance attestation chain would
need re-verification (which would indicate either supply-chain
compromise or an actual bug in the source). Run an internal source
review against the tagged commit; reach out to Microsoft for
forensics details.

## Forward-look: structural FP mitigation

Two structural changes that reduce FP rate to ~zero:

1. **Authenticode signing** — Azure Trusted Signing (~$10/mo,
   1-2 week issue time) signs the MSI and the smirror.exe inside.
   Signed binaries clear Defender ML's "suspicious shape" heuristics.
   This is the right long-term fix.

2. **PE version-info embedding** (`cmd/smirror/versioninfo.json` via
   `goversioninfo`) — embeds CompanyName / ProductName / FileVersion
   into the PE header. Doesn't fix the FP by itself but makes the
   binary look less like an anonymous stripped Go-build to ML
   classifiers. Tracked as R-24 in `docs/release-maturity.md`;
   typically lands together with first signed release for a single
   user-facing publisher transition.
