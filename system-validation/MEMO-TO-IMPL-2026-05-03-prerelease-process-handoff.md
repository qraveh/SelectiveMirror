========================================================================
TO:   SelectiveMirror implementation session
FROM: SelectiveMirror pre-release process panel session
      (original panel-review pre-release 2026-04-28 +
       pre-release status panel 2026-05-03 +
       golangci-lint debt cleanup)
RE:   Hand-off of the pre-release process work stream into the
      implementation pipeline — what landed, what the dashboard
      says, what's still expected of the impl session before /
      during / after the v1.0.0 tag-push, and a small set of
      decisions the impl session owns.
DATE: 2026-05-03
SOURCE: 0.9.106-dev / tip cf91fdc.
        Three commits on master from this stream:
          9c13d11 (0.9.42-dev)  — original panel-review batch
          57e505d (0.9.103-dev) — pre-release status panel 1-7
          cf91fdc (0.9.106-dev) — golangci-lint debt cleanup
        Twelve commits from the impl session interleaved between
        them (telemetry rounds 4 → 9, MSI icon, FINDING-25 closure,
        etc.); see the round-9 memo from the validation session.
========================================================================

## Why this memo is from a different session than the telemetry memos

The telemetry MEMO-TO-IMPL-* series is the validation-side
counterpart to the impl session's telemetry feature work. This
memo is from a different parallel stream — pre-release process —
that has been touching:

  - `.github/workflows/release.yml`, `release-dryrun.yml`, `sla-smoke.yml`
  - `installer/build-msi.ps1`, `installer/TelemetryConsent.wxi`
  - `docs/operations/release-runbook.md`, `release-day.md`,
    `telemetry-ops.md`
  - `docs/release-maturity.md`
  - `scripts/check-allowlist-vs-changelog.ps1`,
    `extract-changelog.ps1`, `check-pii-leak.ps1`
  - `system-validation/allowlist.txt`
  - `screenshots/README.md` (new convention dir)
  - `.claude/agents/sm-keeper.md`
  - `cmd/smirror/main.go::var version` (per-commit bumps + the
    cmdStatus //nolint)
  - 9 pre-existing lint sites across `internal/` + `cmd/`

Effectively zero overlap with the telemetry stream's edits except
in two places:
  - `cmd/smirror/main.go` (telemetry stream owns most of it; we
    touched only `var version`, `cmdStatus`'s //nolint header, and
    the `statusEmitSanitized` ineffassign fix)
  - `cmd/smirror/cmd_report_bug_submit.go` (telemetry-stream code;
    we touched only the `submittedTier` ineffassign fix and added
    //nolint:unused to two placeholder helpers)

If the impl session sees a cmd/ git-blame line that looks unfamiliar
on a SelectiveMirror v0.9.10x-dev commit, it's likely a lint cleanup
edit (cf91fdc) — not behavior, just lint-shaped.

## TL;DR

The pre-release infrastructure for v1.0.0 tag-day is operational.
Three commits established and then matured it:

  1. **9c13d11** wired the methodology — release-dryrun.yml,
     sla-smoke.yml, runbook, release-day playbook, sm-keeper agent,
     maturity dashboard, version assertion, system-validation gate,
     PII smoke, MSI consent dialog, build-provenance attestations,
     winget regen + gated submission, CHANGELOG-driven release notes.
     24 files; the original PR-R*/Q*/S*/W*/PM* findings closed.
  2. **57e505d** matured the methodology after a derivative panel
     pointed at it — shared allowlist file (eliminated drift between
     release.yml and release-dryrun.yml), allowlist↔CHANGELOG
     linter, extract-changelog strict/AllowMissing semantics, Path A
     signing scaffold (90-day artifact retention + insertion-point
     comments), build-key fingerprint parity in dryrun, gh
     attestation verify roundtrip in CI, screenshots convention,
     Branch-CI row in the dashboard. 10 files; PR-PRE-M1..M4, F2,
     F3, D2, D4 closed.
  3. **cf91fdc** closed the pre-existing golangci-lint debt — 13
     findings across errcheck, gosec G115, unconvert, unused, SA9003,
     SA5011, ineffassign, gocyclo. ci.yml is locally green; flips
     on the next master push (the lint-debt failure-email stream
     stops then).

The impl session can now tag v1.0.0 with the following confidence
chain: dryrun green → tag → release.yml runs the same gates plus
upload + attest + verify-roundtrip → manual publish → release-day
playbook drives T+1h..T+24h checks. The runbook documents every
step; the sm-keeper agent encodes it; the maturity dashboard tracks
the long-pole indicators (SignPath cert is the only 🔴; everything
else is 🟢 or 🟡-with-narrative).

## What the impl session should know about the new infrastructure

The following entry points are most likely relevant to your daily flow:

### Allowlist + linter

The `system-validation/allowlist.txt` file is the SINGLE source of
truth for which `system-validation/Test*` are tolerated as RED at
release time. Both `release.yml` and `release-dryrun.yml` read it.
The companion linter `scripts/check-allowlist-vs-changelog.ps1`
fires before the system-validation step in both workflows; if a
test is in the allowlist but not mentioned anywhere in CHANGELOG,
the release fails fast with a runbook-§3 hint.

Currently the allowlist is empty (BUG-R3-1 closed by decision,
FIND-R4-1 closed by hooks deferral, BUG-R4-1/R5-1/NEW-R10-1 closed
by impl). To re-add an entry: append the test name to allowlist.txt
AND add a CHANGELOG `### Bugs known at tag` bullet that references
the test name verbatim. The linter enforces the agreement.

### Release notes pipeline

`scripts/extract-changelog.ps1 -Version X.Y.Z -Output release-notes.md`
extracts the `## [X.Y.Z]` section from CHANGELOG.md into a file that
release.yml pins as the GitHub Release body via
`gh release edit --notes-file`. The script has TWO modes:

  - Strict (default): if the `## [X.Y.Z]` section is missing, fail
    loudly with a runbook-§2 hint ("did you forget to promote
    [Unreleased]?"). Used by release.yml.
  - Permissive (`-AllowMissing`): fall back to `## [Unreleased]`
    with an explicit "AllowMissing fallback" provenance label in
    the output. Used by release-dryrun.yml for the preview path.

If you ever see a release body that says "Release notes extracted
from CHANGELOG.md [Unreleased] (AllowMissing fallback for [X.Y.Z])"
on a real tag, something is wrong — the strict path got bypassed.

### Build-key fingerprint check

`smirror version` prints `telemetry build-key: <fingerprint>` where
the value is one of:

  - 64-hex string: HMAC derived key embedded via ldflag at release.
  - `none`: ldflag never set (no master key at build time).
  - `invalid`: ldflag set but not valid hex.

`release.yml` rejects any value other than a valid 64-hex unless
the maintainer has explicitly opted out via repository variable
`RELEASE_ALLOW_NO_TELEMETRY_KEY=1`. `release-dryrun.yml` tolerates
`none` (snapshot binary won't ship) but still fails on `invalid`.

If telemetry submission stops working post-release, check this
fingerprint first.

### MSI consent dialog

`installer/TelemetryConsent.wxi` carries the `TelemetryTierDlg`
custom WiX dialog with a three-option radio group bound to
`INSTALL_TELEMETRY_TIER` (default `none`). Sequenced between
LicenseAgreementDlg and InstallDirDlg via `Publish` overrides on
the WixUI_InstallDir chain.

The MSI-table verification (Property + Dialog + 3 RadioButtons + 2
Publish overrides + 11 controls) was done locally during 9c13d11.
Visual install-time test (does the dialog actually render to a real
user?) is in the release-day playbook T+1h step (PR-PRE-D2): the
maintainer takes a screenshot and commits it to
`screenshots/v<version>/install-telemetry-dialog.png` before
clicking Publish.

### SignPath insertion points

`release.yml` has two clearly-marked insertion-point comment blocks
where the SignPath GitHub Action steps land when the EV cert
arrives. Pattern: gate on `vars.SIGNPATH_ENABLED == '1'`, two
action invocations (one for smirror.exe, one for MSI), tied to four
repo-scoped vars + one secret. SECURITY.md has the upstream plan;
release-maturity.md row 1 is the long-pole indicator.

Until the cert lands: the artifact-retention bump (7 → 90 days)
buys you Path A flexibility — the SignPath cert can sign an
already-published v1.0.0's exact bytes weeks later, without a
rebuild. See the Path-A-vs-Path-B-constrained discussion in the
release-maturity.md indicator detail.

### SM-keeper agent (`.claude/agents/sm-keeper.md`)

Three modes encoded:
  - **Mode A** (pre-tag): reads dashboard, picks version, prepares
    CHANGELOG promotion, triggers release-dryrun.yml, reports
    go/no-go. Does NOT push the tag — that's a maintainer act.
  - **Mode B** (tag-day): watches release.yml + msi job, verifies
    draft release, reports publish-readiness. Does NOT publish —
    that's a maintainer click.
  - **Mode C** (maturity refresh): re-evaluates each indicator
    against current evidence; proposes color flips; updates the
    file under maintainer approval.

Per the round-9 panel, Mode A is currently still user-prompt-driven
(PR-PRE-F1, deferred). Pre-tag the maintainer should still type
"is X.Y.Z ready" before tagging.

## What's still expected from the impl session

### Before pushing v1.0.0

1. **Promote CHANGELOG [Unreleased] → [1.0.0]**. The body for [1.0.0]
   is already drafted (CHANGELOG.md lines 8-165). The promotion just
   moves the heading. Bump `var version = "1.0.0"` (drop the -dev
   suffix) in the same commit.
2. **Run release-dryrun.yml**:
   ```
   gh workflow run release-dryrun.yml -f intended-tag=v1.0.0 -f ref=master
   ```
   Wait for green. The new gates that didn't exist on prior runs:
   allowlist linter, build-key fingerprint check, attestation-verify
   roundtrip won't fire (no real release to attest), but the rest do.
3. **Verify the worktree is clean**. The 16+ untracked
   `system-validation/{MEMO-TO-IMPL-*, telemetry-*}` files are yours
   to either commit or .gitignore before tag. Untracked files are a
   release-bar violation per runbook §4.
4. **Confirm the tag-source invariant**: `var version` (stripped of
   -dev if present) MUST equal the tag-without-v. release.yml's
   first step asserts this; it's the v0.9.22-class bug-prevention.

### During / right after tag-push

5. The `release` job and `msi` job run in parallel; total wall-clock
   ~5-7 minutes. release-day.md T-0 through T+15m describes what to
   watch for at each step.
6. Both jobs leave the GitHub Release as **Draft** (per
   `.goreleaser.yaml`'s `draft: true`). The maintainer (you) clicks
   Publish after verifying the draft body, both assets attached, and
   build-provenance attestations are present.
7. winget submission: only fires if you've configured repo variable
   `WINGET_SUBMIT_ENABLED=1` AND secret `WINGET_SUBMIT_PAT`. Both
   are missing today; the manifest is just uploaded as a workflow
   artifact for manual `wingetcreate submit`.

### T+1h synthetic install (PR-PRE-D2 evidence)

8. On a clean Windows VM (or your dev box if you've cleaned prior
   installs), download the MSI from the GitHub release URL, run
   `certutil -hashfile SelectiveMirror.msi SHA256` against
   `checksums.txt`, and run
   `gh attestation verify SelectiveMirror.msi --repo qraveh/SelectiveMirror`.
9. Click through SmartScreen warning (until SignPath lands).
10. **Take a screenshot of the TelemetryTierDlg** when it appears
    in the install wizard. Commit to
    `screenshots/v1.0.0/install-telemetry-dialog.png`.
    This is the artifact that closes maturity dashboard row 3
    (MSI consent UI 🟡 → 🟢) for v1.0.0.

### T+24h telemetry health check

11. Per release-day.md T+24h section. Cloudflare Worker logs +
    Supabase SQL queries. Specifically: did THIS version submit
    anything? Any recurring `bug_kind` cluster on the new version?
12. With the v2 telemetry pipeline live (per the impl session's
    9.99..9.106 work), this is no longer hypothetical — the
    weekly digest will surface anomalies.

## Decisions the impl session may need to make

### D-1: Promote `var version` 0.9.106-dev → 1.0.0 in the same commit as CHANGELOG promotion?

The CHANGELOG `## [1.0.0]` body says (line 54): "Source `cmd/smirror
/main.go::version` bumped 0.9.66-dev → 1.0.0". The current source
is at 0.9.106-dev (we drifted because both this stream and the impl
session kept bumping per-commit). The CHANGELOG narrative needs a
small rewrite to reflect actual final value, OR the version bump
needs to land separately and the narrative needs updating to "0.9.106-
dev → 1.0.0".

Recommendation: the runbook §1 invariant requires
`strip-dev(var version) == tag-sans-v`. `strip-dev("1.0.0-dev")` =
"1.0.0" works; `strip-dev("1.0.0")` = "1.0.0" also works (no -dev
to strip). Either bump shape is fine. Pick `1.0.0` (no -dev) for
the v1.0.0 commit — "release" version, no further dev cycle on
this number.

### D-2: Tag v1.0.0 as stable or v1.0.0-rc1 first?

`.goreleaser.yaml` line 60 has `prerelease: auto` — tags with
`-rc/-beta/-pre` suffix get marked as GitHub prereleases; bare
tags become stable.

Path-B-constrained (preferred per the SignPath discussion): tag
v1.0.0 stable today, unsigned. When SignPath cert arrives, tag
v1.0.0.X with the signing step active. Selfupdate carries users
forward.

Path A: tag v1.0.0-rc1 today as prerelease, then re-tag v1.0.0
once SignPath cert arrives (re-sign in place). Operationally
heavier; only choose if v1.0.0's _label_ specifically must carry
the eventual signature.

Either is supported by the infrastructure. README's audience
banner is calibrated for "maintainer + small group of testers" —
which fits either path's first 30 days.

### D-3: Bring 16 untracked telemetry files into git before tag?

`system-validation/{MEMO-TO-IMPL-*, telemetry-*}` is your
parallel stream's product. The runbook §4 violates "worktree clean"
if these stay untracked at tag. Three options:

  - Commit them all in one batch: "0.9.107-dev: telemetry rounds
    4-9 evidence + harnesses". Standard pattern (matches commit
    1e8eae9 from the v0.9.x cycle).
  - .gitignore the Python/markdown harness output and commit only
    the MEMO-TO-IMPL-* files.
  - Delete the working files and commit nothing (loses the
    audit trail).

Recommendation: option 1. The harness outputs are part of the
v1.0.0 evidence — useful when the v1.0.x panel re-audits.

### D-4: Land SM-keeper Mode A autonomy (PR-PRE-F1) in v1.0.x?

Currently the agent is user-prompt-driven. The maintainer has to
remember to invoke "is X.Y.Z ready". This is exactly the burden
the agent was supposed to remove. Two implementations:

  - Git pre-push hook that fires sm-keeper Mode A when pushing a
    tag-promotion commit.
  - Scheduled-task cron (15-min ticks) that pings if `var version`
    strip-dev hasn't matched a tag in N days.

Both are cheap. Neither is in scope for v1.0.0. Defer to v1.0.1
unless you find yourself skipping the dryrun under time pressure
between v1.0.0 and v1.0.1.

## Items deferred per pre-release status panel item 8

These are open from the 2026-05-03 panel and were explicitly
labeled "v1.0.x" without a closure commit. Listed for impl-session
visibility, not for action this cycle:

  - **PR-PRE-M5** — inter-job artifact hash-pinning (low likelihood
    of attack; trivial to add when wanted)
  - **PR-PRE-D1** — tag-deletion → orphan-attestation procedure
  - **PR-PRE-D3** — draft-release publish SLA
  - **PR-PRE-B1** — pre-tag wall-clock cost (caching, fast/full lanes)
  - **PR-PRE-B2** — system-validation runs 3× per tag (commit-SHA cache)
  - **PR-PRE-B3** — maturity dashboard refresh schedule (cron)

## What I deliberately did NOT do

- **Did not modify `cmd/smirror/cmd_telemetry.go`** — your stream
  owns it. There was an uncommitted edit by your session at the
  start of this batch; status is now clean (you committed it).
- **Did not stage or commit the 16+ untracked
  `system-validation/{MEMO-TO-IMPL-*, telemetry-*}` files** —
  they're your stream's working products. Decision D-3 above.
- **Did not bump `var version` to 1.0.0** — that's the tag-day
  promotion act. I bumped along the dev cadence (0.9.42 → 0.9.103
  → 0.9.106).
- **Did not push any tag** — sm-keeper.md explicitly forbids
  agent-initiated tag pushes. Per design.
- **Did not flip `release-maturity.md` row 1 (Code Signing)** —
  the SignPath cert hasn't arrived; the row stays 🔴 with the
  long-pole pointer.
- **Did not implement SignPath itself** — only the scaffold
  (insertion-point comments, retention bump, dashboard row).
  Activation is a one-PR delta when the cert lands.
- **Did not refactor `cmdStatus`** — added `//nolint:gocyclo`
  with a v1.0.x-cleanup TODO. The function is sequential reporting
  code; the complexity is intrinsic to the surface.
- **Did not retire `installMethodHint` / `platformLabel`** — they
  carry `//nolint:unused` with rationale; both are placeholders for
  future build-time injection. If your INSTALL_METHOD ldflag work
  doesn't materialize this cycle, consider deleting them next pass.

## Appendix: SignPath integration deep-dive

This appendix exists because the SignPath insertion-point comment
blocks in `release.yml` are correct but minimal. When the EV cert
arrives, you need more than just "uncomment those steps." Below is
the full picture in three parts: what to send to SignPath, what
they give you, and how to wire it without corrupting the MSI.

### A.1 Applying to SignPath Foundation

Eligibility (per `about.signpath.io/foundation`):

  - OSI-approved license (MIT qualifies)
  - Public, active source repository (GitHub counts)
  - Transparent CI build pipeline (GitHub Actions counts)
  - Real users / real purpose (not vanity / abandoned)
  - Maintainer agrees to AUP (no signing of unrelated artifacts,
    no key-leak-implying behavior)

The application form fields (current as of 2026-05; SignPath may
have changed them — treat as a checklist, not a literal form):

  - Project name: `SelectiveMirror`
  - Repo URL: `https://github.com/qraveh/SelectiveMirror`
  - License + URL: MIT, link to `LICENSE`
  - Short description: "Real-time selective file synchronization
    for Windows via rclone — open-source CLI + WiX MSI installer;
    Windows-first, Go-based"
  - Owner / contact: smirror@qodeh.com + GitHub handle
  - Use case for signing: "v0.9.x users hit Defender SmartScreen
    on every install of the unsigned MSI; project is Windows-first;
    signing is the path to drop the friction"
  - CI/CD pipeline: "GitHub Actions; release.yml triggers on
    `tags: ['v*']`; insertion point already pre-positioned at
    `release.yml` lines [search for SIGNPATH_ENABLED]"
  - Artifacts to sign: `smirror.exe` (PE x64) + `SelectiveMirror.msi`
    (WiX 6 MSI x64)
  - Build-artifact URL pattern: `releases/download/v<version>/`
  - Release cadence: weekly currently; monthly post-v1.0
  - Audience: maintainer + small group of testers; widening planned

Helpful evidence to link from the application:
  - `SECURITY.md` § Code Signing (the plan)
  - `docs/release-maturity.md` row 1 (the dashboard tracking)
  - The insertion-point comment block in `release.yml`

Review timeline: 1-3 weeks typical. Reviewers may ask follow-up
questions; respond promptly. Common rejection reasons:
project-looks-abandoned, dual-license-with-commercial, use-case
framing implies something other than end-user trust.

### A.2 What you receive on approval

| Item | Type | Where it lives | What you do with it |
|---|---|---|---|
| Account | Web UI tenancy | `app.signpath.io` | Log in to manage everything below |
| Organization | Top-level tenant + UUID | SignPath portal | UUID → repo variable `SIGNPATH_ORGANIZATION_ID` |
| Project | Child of org, slug `selectivemirror` | SignPath portal | Slug → repo variable `SIGNPATH_PROJECT_SLUG` |
| Signing policy | Rules for who-can-submit + approval gating + retention | SignPath portal | Slug → repo variable `SIGNPATH_SIGNING_POLICY_SLUG`. **Configure with auto-approval for the release pipeline**, otherwise CI hangs |
| Artifact configurations | Per-format rules: one for PE (`smirror-exe`), one for MSI (`smirror-msi`) | SignPath portal | Slugs → `artifact-configuration-slug` field of the GitHub Action |
| EV certificate | The actual cert + private key | SignPath HSM (you NEVER download it) | Authenticated signing requests use it; the public chain ends up embedded in your signed artifacts via Authenticode |
| API token | OAuth-style bearer; scoped to your project + signing policy | Stored once as GitHub Actions secret `SIGNPATH_API_TOKEN` | Provided to the signpath GitHub Action via `with: api-token: ${{ secrets.SIGNPATH_API_TOKEN }}` |

The cert is the only "thing with the private key" and it lives on
SignPath's HSM permanently. There is no `.pfx` to download, no
password to type, no per-machine import. The API token is the only
secret you handle; project-scoped, leaks limited to "attacker can
submit signing requests against your project, subject to policy
approval gating + audit log."

What you receive back from each signing request:
  - The signed artifact (same bytes + Authenticode appended)
  - A signing-request record in the SignPath UI (audit log)
  - Optional CMS sidecar file (rarely needed for Windows)

### A.3 Authenticode — what it does to the file

**For `smirror.exe` (PE binary)**:
  - PE format has a "certificate table" at
    `IMAGE_DIRECTORY_ENTRY_SECURITY` (end of file, outside loaded
    image regions).
  - Signing computes SHA-256 of the file EXCLUDING the cert-table
    area; SignPath HSM signs that hash; CMS blob (with cert chain)
    is APPENDED to the file in the cert-table area.
  - PE header's cert-table pointer + size are updated.
  - File grows by ~7-12 KB. **Code bytes (`.text`, etc.) unchanged**
    — the binary runs identically signed or unsigned.

**For `SelectiveMirror.msi`**:
  - MSI is a Microsoft Compound Document (structured-storage
    container).
  - Signing computes a hash of the MSI structure EXCLUDING the
    `_DigitalSignature` and `_MsiDigitalSignatureEx` streams.
  - Signature stored as the `_DigitalSignature` stream; optionally
    `_MsiDigitalSignatureEx` (extended; stronger; covers
    SummaryInformation too — SignPath's MSI artifact-configuration
    typically emits both).
  - The MSI's existing streams (the cab containing `smirror.exe`
    etc.) are NOT modified. **MSI signature does NOT also sign the
    contained files** — those need to be signed separately, BEFORE
    being put into the cab.

Verification: `signtool verify /pa /v <file>` walks the chain.

### A.4 Order of operations (the only correct sequence)

```
1. Build smirror.exe                        ← unsigned bytes
2. Sign smirror.exe via SignPath            ← exe now has Authenticode
3. Build MSI from the SIGNED smirror.exe    ← MSI cab embeds signed exe
4. Sign SelectiveMirror.msi via SignPath    ← MSI also has _DigitalSignature
5. Run MSI smoke test against signed MSI    ← exercises signed installer
6. Upload signed MSI to GitHub release
7. Re-bundle GoReleaser ZIP with signed exe ← otherwise ZIP carries unsigned
8. Generate build-provenance attestation    ← attests SIGNED bytes
9. gh attestation verify roundtrip          ← chain-verifies attestation
10. winget manifest regen with signed SHA   ← winget points at signed bytes
```

Each step out of order produces a specific failure mode:
  - 3 before 2 → MSI's embedded exe is unsigned; signing the MSI
    later only signs the outer container, not the cab contents
  - 5 before 4 → smoke test runs against unsigned MSI; signing-
    breaks-something at install discovered at user-install time
  - 6 before 4 → publishes the unsigned MSI; Path-A-clobber
    territory once on the CDN
  - 7 skipped → ZIP users get unsigned exe (no SmartScreen on `.zip`
    extraction, but the exe-on-PATH still triggers it)
  - 8 before 4 → attestation attests the unsigned-MSI hash; users
    who download the signed MSI and run `gh attestation verify`
    get "no matching attestation"

### A.5 Five concrete ways to corrupt the MSI

These are the failure modes that send you back to step 1 with a
deleted tag:

  1. **Re-bundling MSI components after signing** — any post-
     signing step that calls `dotnet build` / `wix build` against
     `Package.wxs` rebuilds the MSI, dropping the signature. Once
     signed, the MSI is read-only.
  2. **Re-zipping a signed MSI without preserving bytes** —
     `Compress-Archive -CompressionLevel Optimal` may re-encode.
     Use `-CompressionLevel NoCompression` if you must, or just
     don't re-zip the MSI (winget + direct download is the path).
  3. **Antivirus quarantine mid-pipeline** — Defender on
     windows-latest scans the MSI right after build. Stage signed
     copies under `signed/` (not `installer\bin\Release\`, which is
     more likely to trip AV).
  4. **`signtool verify` from an old SDK** — if the artifact-
     configuration enables `_MsiDigitalSignatureEx` but your verify
     step uses an old signtool, it fails and you think signing
     broke. Use latest Windows SDK signtool.
  5. **Bundling files into the MSI cab AFTER signing** — no
     legitimate reason in your pipeline, but: this requires
     re-signing.

### A.6 Concrete `release.yml` deltas (post-approval, ~30 lines)

In the `release` job, between `Locate built smirror.exe` and
`Upload smirror.exe artifact for msi job`:

```yaml
- name: Sign smirror.exe via SignPath
  if: ${{ vars.SIGNPATH_ENABLED == '1' }}
  uses: signpath/github-action-submit-signing-request@v1
  with:
    api-token:                   ${{ secrets.SIGNPATH_API_TOKEN }}
    organization-id:             ${{ vars.SIGNPATH_ORGANIZATION_ID }}
    project-slug:                ${{ vars.SIGNPATH_PROJECT_SLUG }}
    signing-policy-slug:         ${{ vars.SIGNPATH_SIGNING_POLICY_SLUG }}
    artifact-configuration-slug: smirror-exe
    github-artifact-id:          ${{ steps.upload-unsigned-exe.outputs.artifact-id }}
    wait-for-completion:         true
    wait-for-completion-timeout-in-seconds: 600
    output-artifact-directory:   signed/

- name: Replace built exe with signed copy + verify
  if: ${{ vars.SIGNPATH_ENABLED == '1' }}
  shell: pwsh
  run: |
    Copy-Item signed/smirror.exe "${{ steps.locate-exe.outputs.EXE_PATH }}" -Force
    & "C:\Program Files (x86)\Windows Kits\10\bin\x64\signtool.exe" verify /pa /v "${{ steps.locate-exe.outputs.EXE_PATH }}"
    if ($LASTEXITCODE -ne 0) { Write-Error "signtool verify failed on signed exe"; exit 1 }
```

Then the GoReleaser ZIP needs re-bundling because GoReleaser already
zipped the unsigned exe:

```yaml
- name: Re-bundle GoReleaser ZIP with signed exe
  if: ${{ vars.SIGNPATH_ENABLED == '1' }}
  shell: pwsh
  env:
    GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
  run: |
    $zip = Get-ChildItem dist -Recurse -Filter '*.zip' | Select-Object -First 1
    if (-not $zip) { Write-Error "GoReleaser zip not found"; exit 1 }
    Compress-Archive -Update -Path "${{ steps.locate-exe.outputs.EXE_PATH }}" -DestinationPath $zip.FullName
    gh release upload ${{ github.ref_name }} $zip.FullName --clobber
```

In the `msi` job, between `Build MSI` and `MSI smoke test`:

```yaml
- name: Sign SelectiveMirror.msi via SignPath
  if: ${{ vars.SIGNPATH_ENABLED == '1' }}
  uses: signpath/github-action-submit-signing-request@v1
  with:
    api-token:                   ${{ secrets.SIGNPATH_API_TOKEN }}
    organization-id:             ${{ vars.SIGNPATH_ORGANIZATION_ID }}
    project-slug:                ${{ vars.SIGNPATH_PROJECT_SLUG }}
    signing-policy-slug:         ${{ vars.SIGNPATH_SIGNING_POLICY_SLUG }}
    artifact-configuration-slug: smirror-msi
    github-artifact-id:          ${{ steps.upload-unsigned-msi.outputs.artifact-id }}
    wait-for-completion:         true
    wait-for-completion-timeout-in-seconds: 600
    output-artifact-directory:   signed/

- name: Replace MSI with signed copy + verify
  if: ${{ vars.SIGNPATH_ENABLED == '1' }}
  shell: pwsh
  run: |
    Copy-Item signed/SelectiveMirror.msi installer\bin\Release\SelectiveMirror.msi -Force
    & "C:\Program Files (x86)\Windows Kits\10\bin\x64\signtool.exe" verify /pa /v installer\bin\Release\SelectiveMirror.msi
    if ($LASTEXITCODE -ne 0) { Write-Error "signtool verify failed on signed MSI"; exit 1 }
```

Note the `github-artifact-id` mode: it requires the unsigned exe /
MSI to have been uploaded as a workflow artifact FIRST, then
referenced by the SignPath action. The current `release.yml`
already uploads the exe as `smirror-exe-<version>` — capture that
step's `outputs.artifact-id` and reference it. Same pattern for
the MSI (add an unsigned-MSI upload step before the signing step).

After these inserts, all the existing downstream steps (smoke test,
upload to release, attestation, attestation-verify, hash compare,
winget regen) operate on the SIGNED bytes — no other changes
needed.

### A.7 Five practical gotchas not covered above

  1. **Approval gating** — SignPath signing policies can require a
     human approval click before signing actually runs. For
     automated CI this hangs the workflow. Configure the policy
     with auto-approval for tags pushed by qraveh on the
     SelectiveMirror repo. Keep a separate "manual-approval-
     required" policy for any one-off signing you do locally.
  2. **Signing time variance** — typical 30-90 seconds; cold cache
     or first-of-the-day requests can take 3-5 minutes. Use
     `wait-for-completion-timeout-in-seconds: 600` (10 min).
  3. **Cert renewal** — SignPath Foundation certs are issued for
     1-3 years. Rotation is HSM-side, no workflow changes needed.
  4. **Audit log** — every signing request shows in the SignPath UI
     permanently. Add a release-day playbook step "verify signing
     request record exists" — defensive at no operational cost.
  5. **First signed release still has SmartScreen friction** —
     SmartScreen tracks per-cert-subject reputation. EV certs
     accumulate reputation faster than OV (Microsoft Defender
     warns less aggressively from day 1, but full reputation takes
     ~3 releases under the cert). Plan accordingly: don't expect
     "tag the first signed release and SmartScreen disappears."

### A.8 What `release.yml` already provides for SignPath

The 9c13d11 + 57e505d work pre-positioned everything you need:
  - Insertion-point comment blocks at the canonical positions,
    gated on `vars.SIGNPATH_ENABLED == '1'`. You uncomment + fill
    in the action invocations.
  - Artifact retention bumped 7 → 90 days — buys Path A re-sign
    flexibility if SignPath arrives weeks after a tag.
  - `gh attestation verify` step runs after every attestation —
    catches "signed MSI hash doesn't match attestation subject"
    immediately if order-of-operations is wrong.
  - PR-Q5 published-MSI hash verify catches the case where signing
    succeeded but a later step accidentally reverted to unsigned
    bytes.

The integration is a ~30-line workflow delta plus one-time
SignPath-portal config. No new infrastructure to invent.

## Closing note

After three commits, the pre-release-process methodology is at
steady-state for v1.0.0 tag-day:

  - Dryrun before every tag (PR-R1)
  - Tag-source assertion (PR-R2)
  - Single-binary handoff (PR-R3)
  - System-validation gating with shared allowlist (PR-Q3 + PR-PRE-M1)
  - Allowlist↔CHANGELOG linter (PR-PRE-M3)
  - PII smoke (PR-S5)
  - HMAC fail-loud + build-key fingerprint (PR-S3 + PR-PRE-M2)
  - Build-provenance attestations + verify roundtrip (PR-S4 + PR-PRE-M4)
  - Post-publish hash verify (PR-Q5)
  - Winget regen (PR-W1)
  - CHANGELOG-driven release notes (PR-W4 + PR-PRE-F3)
  - MSI consent dialog (PR-S2)
  - SignPath scaffold (PR-PRE-F2)
  - Compatibility/rollback in README (PR-W2)
  - Audience banner (PR-PM2)
  - Known issues inventory (PR-PM1)
  - Runbook + release-day playbook + maturity dashboard +
    SM-keeper agent (PR-PM3 + PR-PM4)
  - Screenshots convention (PR-PRE-D2)
  - Branch-CI surfaced (PR-PRE-D4)
  - golangci-lint debt: 13 findings closed (cleanup batch)

Anything that breaks this loop is a process bug. Either:
  - Surface to the SM-keeper agent (Mode A: "is X.Y.Z ready").
  - Open a panel-review for the methodology (the recursive
    pre-release-status panel pattern).
  - Or just file it as a finding in the next CHANGELOG `### Bugs
    known at tag` block with a test name + allowlist entry.

The implementation session owns the tag-push and the publish-click.
Everything else has a runbook entry, a workflow gate, or a
dashboard row. No methodology gap that I am aware of for v1.0.0
specifically.

— pre-release process panel session, 2026-05-03
   (rounds: 2026-04-28 panel + 2026-05-03 status panel + lint debt
   cleanup; commits 9c13d11, 57e505d, cf91fdc on master)
