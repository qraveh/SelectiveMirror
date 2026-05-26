# WDSI false-positive submission — v1.0.60

**Purpose.** Microsoft Defender flagged the v1.0.60 MSI as
`Trojan:Win32/Wacatac.B!ml` on first user download from
`release-assets.githubusercontent.com`. The `!ml` suffix marks this as a
machine-learning heuristic detection, not a signature match. This
document is a ready-to-paste submission template for the Microsoft WDSI
portal so the v1.0.60 SHA gets reclassified.

**Context** — v1.0.59 was previously flagged with the same threat and
was withdrawn before public release. v1.0.60 shipped with two
source-side ML mitigations: PE VERSIONINFO embedding (R-24, via
`goversioninfo`) and unstripped symbol table (R-25, `-s -w` removed
from `.goreleaser.yaml`). Local Defender scan of the v1.0.60 build
returned `found no threats` — but cloud Defender (fresher ML models +
reputation signals + download context) still flags v1.0.60 on first
download. R-24 + R-25 reduced FP rate but did not close the gap.
Authenticode signing remains the durable fix; see
`memory/signing_provider_update_2026-05-26.md` for the revised
provider matrix.

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
| **Product version** | 1.0.60 |
| **Submitter name** | Raveh Neeman |
| **Submitter email** | smirror@qodeh.com |
| **Submitter role** | Software developer (project maintainer) |

## File uploads

Upload TWO files (both via the form, multiple attachments):

1. **`SelectiveMirror.msi`** — the released installer.
   - SHA-256: `a0d8252b8cb5d9a7934e51991bd16e5d4ba5ae9f5f9abfacaab25a87c5ea02f3`
   - Size: 9,150,464 bytes (~8.73 MB)
   - Download: https://github.com/qraveh/SelectiveMirror/releases/download/v1.0.60/SelectiveMirror.msi
   - If Defender quarantines on download, restore via Windows Security
     → Protection History → click the Wacatac entry → "Allow on device",
     then re-download / use the restored file.

2. **`smirror.exe`** — the binary inside the MSI.
   - Extract from the MSI via
     `msiexec /a SelectiveMirror.msi /qn TARGETDIR=<temp>` or via
     7-Zip / lessmsi.
   - The binary is also the entire content of
     `SelectiveMirror_windows_amd64.zip`.

## Comments / additional details (paste into the form's "additional details" field)

```
SelectiveMirror is an MIT-licensed open-source file-mirroring tool
for Windows. https://github.com/qraveh/SelectiveMirror

The v1.0.60 MSI was built by GitHub Actions CI from public source
code at the tagged commit. Build provenance attestation is available
via:

    gh attestation verify SelectiveMirror.msi --owner qraveh

(exits with code 0 confirming the binary came from this repository's
CI pipeline on the tagged commit; SLSA Provenance v1 envelope.)

Detection details:
- Detection name: Trojan:Win32/Wacatac.B!ml
- File:           SelectiveMirror.msi (8.73 MB)
- SHA-256:        a0d8252b8cb5d9a7934e51991bd16e5d4ba5ae9f5f9abfacaab25a87c5ea02f3
- Build pipeline: GitHub Actions workflow `release.yml`
- Build runner:   ubuntu-latest (Go) + windows-latest (WiX MSI build)
- Tagged commit:  4feb38541ebba110efdea17b0ee8c6a3fee7c5fd (v1.0.60)
- Source repo:    https://github.com/qraveh/SelectiveMirror

The binary is unsigned. Authenticode signing is pending: the project
applied to SignPath Foundation but the foundation rejected the
application on reputation grounds (small OSS project, insufficient
external references on 2026-05-21). The originally-planned Microsoft
Trusted Signing fallback turned out to be unavailable for Israeli
individual subscribers as of 2026-05 (Microsoft Trusted Signing
limits individual onboarding to USA/Canada and has paused new
individual onboarding entirely). The revised plan procures an OV
code-signing cert from SSL.com or Sectigo; provisioning is in
progress.

The v1.0.60 build already includes the two source-side ML mitigations
recommended for unsigned Go binaries:

  - PE VERSIONINFO embedded via goversioninfo (CompanyName, ProductName,
    FileVersion 1.0.60.0, OriginalFilename smirror.exe, copyright,
    project-URL comment).
  - Symbol table preserved (no `-s -w` in ldflags) so the binary does
    not look stripped/obfuscated to ML classifiers.

These mitigations defeated the local Defender ML on v1.0.60
(MpCmdRun.exe /Scan /ScanType 3 /File <msi> reports "found no
threats"), but cloud Defender (which uses fresher ML models +
reputation signals + download context) continues to flag v1.0.60 on
first download — apparently because the SHA is novel and there is
no Authenticode signature establishing publisher reputation.

The `!ml` machine-learning detection is the second detection in the
v0.4 → v1.0.0 → v1.0.60 history of any SelectiveMirror release
binary (the first was the withdrawn v1.0.59 with the same threat
name). The project has never bundled, dropped, fetched, executed,
or injected any third-party payload. It is a single Go binary that
watches local file changes (via Windows ReadDirectoryChangesW),
filters them through gitignore-style rules, and invokes the external
`rclone` executable (user-provided) to copy stable files to a
user-configured cloud backend.

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

## Forward-look: durable FP mitigation

The single structural change that drops FP rate to ~zero is
**Authenticode code-signing**. Per
`memory/signing_provider_update_2026-05-26.md` and SECURITY.md, the
revised provider plan is:

1. **OSSign** — free OSS code-signing (ossign.org); criteria unverified.
2. **SSL.com OV** — ~$249/year, individual or organization, cloud HSM.
3. **Sectigo OV** — ~$200/year, similar profile.
4. **Certum Open Source** — ~€30/year + USB token; breaks cloud-CI.
5. **Reapply to SignPath Foundation** in 6-12 months.

R-24 (PE VERSIONINFO) and R-25 (no symbol-strip) remain in place and
help on the local-ML layer. They are not sufficient for cloud Defender
on a fresh unsigned SHA — only Authenticode + reputation accumulation
closes that gap.
