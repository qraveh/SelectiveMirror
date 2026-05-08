# PROPOSAL — Binary MSI consent dialog (v1.0.1+)

**Status**: APPROVED + IMPLEMENTED. Shipped in commit `979697b`
(2026-05-08); canonical BugTracker record is **SM-217**
(`C:\BugTracker\projects\SelectiveMirror\SM-217.md`).
**Author**: Raveh + Claude, 2026-05-03 (after v1.0.0 ship).
**Affects**: `installer/TelemetryConsent.wxi`, `installer/build-msi.ps1`,
`docs/PRIVACY.md` "Three tiers" section, `docs/cli-telemetry-command.md`.
**Does NOT affect**: server-side schema, on-disk client behavior, the
existing CLI three-tier surface, or any opted-in user's tier choice.

---

## Problem statement

The v1.0.0 MSI installer presents three radio choices for telemetry:

```
( ) None         — Send nothing
( ) Standard     — Bug reports + 2 install events ever
( ) Reliability  — Above + bucketed reliability deltas at upgrades
```

This is a **UX-psychology mistake** — independently from any
implementation gap. Six failure modes overlap:

| # | Failure | Mechanism |
|---|---|---|
| 1 | **Middle-option default effect** | When users see 3 choices, they disproportionately pick the middle one because it feels "safe." Standard adoption is artificially inflated; the choice isn't really informed. |
| 2 | **"More is better" anchoring** | Reliability above Standard implies "more help = more virtuous." Users wanting to be helpful pick Reliability without understanding what's added. |
| 3 | **Decision paralysis** | A binary decision is fast. Three options forces comparison, reading, weighing — then most users skim and pick by feel. |
| 4 | **Scale-label confusion** | "Standard" / "Reliability" describe *amount* not *content*. Real privacy-relevant difference is "what dimensions are sent," but the labels obscure that. |
| 5 | **Asymmetric privacy cost** | None ↔ Standard is a *categorical* line (something vs nothing). Standard ↔ Reliability is a *degree* line (more dimensions). Treating both as equal radio choices flattens the distinction the user actually cares about. |
| 6 | **v1.0.0 specifically — empty distinction** | `reliability_snapshot` is deferred to v1.0.x. Reliability and Standard produce identical wire output today. The dialog claims a distinction that doesn't exist. |

The architectural truth in v1.0.x is binary: **share anonymous bug + version
counts, or don't.** Reliability tier is power-user opt-up that doesn't
belong in the installer's first-impression surface.

---

## Multi-role panel analysis

### 🎨 UX (Sasha)

The 3-radio pattern is a known anti-pattern in consent dialogs.
Hick's Law: time to decide grows with log(N) of options. For an MSI
where the user just wants smirror installed, every additional radio is
friction. Two radios with clear labels = 2-second decision.

The visual layout amplifies the middle-option effect: the eye lands
on the middle radio first (Fitts' Law for vertically-stacked
options), and the middle option becomes "safe by location."

Recommendation: **two radios, default `Off`, label them by what they
mean to the user, not by tier name.**

### 🔒 Privacy (Quincy)

The 3-radio surface technically gives the user more choice, but
"more choice in a domain the user doesn't understand" produces worse
consent, not better. PRIVACY.md's binding commitments around what
data is sent are the same regardless of dialog presentation.

The architecture has THREE consent surfaces; only one is the
installer:

  1. **MSI installer** (one-time, at install)
  2. **`smirror telemetry status` / `... <tier>`** (CLI, anytime)
  3. **`smirror report-bug --submit --one-shot`** (per-event)

Surface #1 is a "first impression / informed default" surface. #2 is
the maintained-choice surface. The 3-tier richness belongs at #2,
not #1. Reducing #1 to binary doesn't reduce informed consent — it
*improves* it by making the binary choice (something vs nothing)
the focus.

Recommendation: **two-radio MSI; preserve all three CLI tiers.**

### 📊 PM (Victor)

Counterintuitive but true: binary consent typically produces
*better-quality* contributions than 3-tier. Reasoning:

  - Users who explicitly opt in to "share anonymous bug + version
    data" understand what they're contributing — their consent is
    meaningful.
  - Users who default to the middle option in a 3-tier dialog
    contribute the same data without informed consent. Their
    contributions are statistically the same but ethically worse.

For SelectiveMirror as a single-maintainer OSS project: the
maintainer doesn't need volume; the maintainer needs ground-truth
counts they can trust. Trustworthy counts come from informed
consent. Binary maximizes informed consent.

Recommendation: **binary in MSI; expect contribution rate to *drop*
slightly while the contributing-population's informedness *rises*
substantially.**

### 🛠 FAE (Felix)

What users see today: most click "Next" through the MSI without
reading. Whatever's pre-selected is what ships. Default in v1.0.0 is
`None` so the worst-case is "user opts out by default and has to
actively opt in" — that's the privacy-respectful direction.

The 3-tier dialog adds a worry surface: a non-trivial fraction of
users who DO read it land on Reliability because "more help = more
virtuous." They don't understand they're committing to bucketed
reliability deltas at every upgrade. They might unknowingly install
this on a machine where they have privacy concerns, and only realize
later.

Recommendation: **binary surface in MSI is friendlier even for the
user who reads carefully.**

### 🏛 Architecture (Mary)

The server schema and contribute() RPC support all three tier
contributions equally. Demoting Reliability from MSI to CLI-only is
a presentation change, not an architectural one. The
`telemetry.bug_report.submitted_tier` ENUM (which today has values
`'standard' | 'reliability' | 'one_shot'`) keeps Reliability
available for any contribution path that uses it.

The MSI today writes `HKLM\Software\SelectiveMirror\TelemetryTier`
with one of `none | standard | reliability`. Restricting MSI to
`none | standard` is a one-line change in the WiX dialog.

Recommendation: **registry surface stays; MSI dialog tightens.**

### 📚 Tech Writer (Paige)

Label phrasing matters. Current labels ("None / Standard /
Reliability") are scale labels; they don't say what's contributed.
For a binary surface I'd write:

  - **Affirmative**: "Help improve SelectiveMirror — share anonymous
    bug + version counts" (with concrete enumeration of what's
    NOT sent, as currently in PRIVACY.md).
  - **Default**: "Don't share anything — nothing leaves your machine
    until you change this via `smirror telemetry`."

Both labels self-describe. Neither implies "more is better." The
default is recognizable as the privacy-default.

Avoid:
  - "Off" / "On" — too cold; doesn't tell users what they're
    helping with.
  - "Standard" / "Maximum" — re-introduces the scale-label problem.
  - Tier-named labels in the MSI ("None / Standard") — couples the
    installer to internal terminology the user doesn't need to know.

Recommendation: **descriptive labels, not tier names; lead with the
verb (help / don't share).**

### ⚔ Adversary (Quinn)

Failure modes I'd worry about:

  1. **A user who chose Reliability in v1.0.0 upgrades to v1.0.1+
     and the new MSI doesn't show Reliability** → if we silently
     downgrade them, that's a privacy violation in reverse ("you said
     yes to X, we changed it to Y"). Mitigation: **don't change
     existing tier**. The new MSI dialog is shown only on fresh
     install, or when the registry is unset.

  2. **A user who chose Standard in v1.0.0 might wonder: "wait, is
     my tier still active? does the new dialog mean I'm being
     re-consented?"** Mitigation: skip the dialog entirely on upgrade
     flows where a tier is already set.

  3. **An attacker creates a malicious MSI that does present 3
     options, including a covert "send everything" tier** → outside
     the threat model of this proposal (signed-MSI threat model is
     handled by SignPath + winget; this is about the canonical
     Authenticode-signed MSI we ship).

  4. **Reliability tier never gets used after this change** because
     CLI-only options are discoverable only by power users. If the
     maintainer wants reliability data, they need to actively
     promote `smirror telemetry reliability` (in `--help`, in the
     v1.0.x release notes, in `smirror telemetry status` output).
     Mitigation: deliberately design Reliability as
     "advanced-operator opt-up," accept the lower volume, value the
     informedness more.

Recommendation: **honor existing choices; don't re-prompt; design
Reliability as a known advanced-operator path.**

---

## Recommended solution — Option D: binary in MSI, three tiers in CLI

The MSI dialog presents two clearly-labeled radios; CLI keeps all
three tiers; existing tier choices (including Reliability) are
preserved across upgrade.

### Proposed dialog content

```
+--------------------------------------------------------------+
|  SelectiveMirror Setup — Help improve SelectiveMirror?       |
|                                                              |
|  SelectiveMirror is free and open-source. The maintainer    |
|  uses anonymous counts to prioritize fixes.                  |
|                                                              |
|  ( ) Don't share anything (default)                          |
|       Nothing leaves your machine. You can change this       |
|       later via `smirror telemetry`.                         |
|                                                              |
|  (●) Share anonymous bug + version counts                    |
|       Helps the maintainer see which bugs and versions       |
|       are common. We never collect: file names, paths,       |
|       contents, your name / email / IP, narratives, or       |
|       hardware fingerprints.                                 |
|                                                              |
|  [ Read full privacy policy ]                                |
|                                                              |
|                              [ Back ]  [ Next ]  [ Cancel ]  |
+--------------------------------------------------------------+
```

Default radio: "Don't share anything." Visually first, visually
selected on dialog open. The maintainer's question — *"do I have
permission to count you?"* — is presented as a binary, not a scale.

### Mapping to the three-tier CLI

| MSI selection | Registry value | CLI tier | What ships on the wire |
|---|---|---|---|
| Don't share anything | `none` | `none` | Nothing. Same as today. |
| Share anonymous data | `standard` | `standard` | Bug reports + first_seen + upgrade events. Same as today's Standard. |
| (not in MSI) | `reliability` | `reliability` | Above + reliability_snapshot at upgrade (when reliability_snapshot writer ships in v1.0.x). Reachable only via `smirror telemetry reliability`. |

`smirror telemetry status` continues to show the actual tier
(including Reliability when set). The MSI just doesn't surface
Reliability as a fresh choice.

### Reliability as an opt-up path

Power users who want to contribute reliability data run:

```bash
smirror telemetry reliability
```

The CLI's existing `cmdTelemetrySet` handler already implements this
path; no code change needed. Discoverability comes from:

  - `smirror telemetry --help` enumerates the three tiers
  - `smirror telemetry status` (at Standard tier) prints a "Want to
    help more? Run `smirror telemetry reliability`" hint
  - PRIVACY.md "Three tiers" section explains the CLI-only third
    tier and its purpose

This is the intended UX: Reliability is an explicit operator
action, not a checkbox in an installer the user clicks past.

---

## Migration story

The proposal is non-breaking for any existing tier choice.

### Fresh install (v1.0.1+ MSI on a machine with no prior smirror)

  - Dialog appears with two radios.
  - Default: "Don't share anything" → `HKLM\...\TelemetryTier = none`.
  - User picks Yes → `HKLM\...\TelemetryTier = standard`.

### Upgrade install (v1.0.1+ MSI on a machine with v1.0.0 already
installed and tier set)

  - MSI custom action reads existing `HKLM\...\TelemetryTier`.
  - If set (any value: `none`/`standard`/`reliability`): skip the
    dialog entirely. Preserve the user's prior choice.
  - If unset (rare; only if registry was tampered): show the dialog
    as for a fresh install.

The user who chose Reliability in v1.0.0 keeps Reliability after the
v1.0.1+ upgrade. The dialog they DON'T see is the same as not
re-prompting them — it's a courtesy, not a downgrade.

### Edge case — silent install (`msiexec /quiet INSTALL_TELEMETRY_TIER=...`)

The MSI property `INSTALL_TELEMETRY_TIER` continues to accept any of
`none`/`standard`/`reliability` for silent / unattended installs.
Power users / IT admins doing scripted deploys can preset
Reliability via the property. The simplification is in the
**interactive** dialog only.

### CHANGELOG entry (v1.0.1 release notes)

```markdown
### Changed

- **MSI installer telemetry consent dialog simplified to two
  choices** ("Don't share anything" / "Share anonymous bug + version
  counts") to address the three-radio middle-option-default effect.
  The CLI continues to support all three tiers; users who chose
  Reliability in v1.0.0 keep Reliability after upgrade. To opt up
  to Reliability tier post-install, run `smirror telemetry
  reliability`. See PROPOSAL-2026-05-03-msi-binary-consent.md.
```

---

## Implementation surface

Concrete file changes for the v1.0.1+ MSI:

### `installer/TelemetryConsent.wxi`

Replace the three-`<RadioButton>` dialog with two. Default `Property
= "INSTALL_TELEMETRY_TIER"` value `"none"`. Affirmative radio writes
`"standard"`.

Rough WiX shape:

```xml
<Control Id="ConsentRadioGroup" Type="RadioButtonGroup"
         X="20" Y="60" Width="320" Height="80"
         Property="INSTALL_TELEMETRY_TIER">
  <RadioButtonGroup Property="INSTALL_TELEMETRY_TIER">
    <RadioButton Value="none"     X="0" Y="0"  Width="300" Height="30"
                 Text="Don't share anything (default)" />
    <RadioButton Value="standard" X="0" Y="40" Width="300" Height="30"
                 Text="Share anonymous bug + version counts" />
  </RadioButtonGroup>
</Control>
```

Plus the surrounding `Text` controls for the descriptive paragraphs
(matching the proposed dialog mockup above).

### `installer/build-msi.ps1`

No change required if the dialog is the only difference. The build
script already passes `INSTALL_TELEMETRY_TIER` as a property; the
list of accepted values doesn't need restriction at this layer
(silent installs can still pass `reliability`).

### MSI custom action — preserve-existing-tier behavior

Add a CustomAction in the `installer/` WiX that runs before the
ConsentDialog:

```
If HKLM\Software\SelectiveMirror\TelemetryTier is set:
    Skip ConsentDialog (don't re-prompt; user already chose)
Else:
    Show ConsentDialog
```

Implementation: a small property-evaluating CustomAction +
`Condition` on the dialog's `Show` event.

### `docs/PRIVACY.md`

Update "Three tiers" section to clarify the dialog surface vs the
CLI surface. Move Reliability from the dialog list to a "Power-user
tier" subsection. Keep the "What you contribute" enumeration for all
three.

### `docs/cli-telemetry-command.md`

Add a "MSI dialog presents two of the three tiers" note in the
"Default tier" section. The CLI surface itself is unchanged.

### Tests

  - `system-validation/TestPanelInstaller_BinaryConsentDialog`:
    grep `installer/TelemetryConsent.wxi` for `<RadioButton`; assert
    exactly 2 radio buttons with values `none` and `standard`.
  - `system-validation/TestPanelInstaller_PreservesExistingTier`:
    smoke-test the registry-preserve custom action by simulating a
    pre-set registry value and asserting the dialog skip happens.
    (May require a Windows-only test fixture.)
  - `cmd/smirror/cmd_telemetry_test.go`: extend
    `TestTelemetryStatus_AfterSetReliability` to assert the
    "Reliability tier ships in v1.0.x" caveat is in the output.

---

## Risks + mitigations

| # | Risk | Severity | Mitigation |
|---|---|---|---|
| R1 | Existing Reliability users feel demoted by the new MSI | Medium | Skip the dialog for upgrades with a tier already set. Don't write the registry. The user's choice is preserved. |
| R2 | Reliability tier becomes invisible — never used | Low-Medium | Surface in `smirror telemetry status` output ("Want to help more?"), in `--help`, in the v1.0.x release notes when reliability_snapshot writer ships. |
| R3 | Affirmative-side contribution rate drops below the maintainer's needs | Low | The maintainer is a single-person OSS project; the maintainer needs trustworthy counts, not maximum volume. Binary consent maximizes informedness. |
| R4 | Users confused by the docs discrepancy ("MSI says 2, CLI says 3") | Low | PRIVACY.md "Three tiers" section explicitly explains the dialog surface vs CLI surface distinction. |
| R5 | Silent-install scripts that pass `reliability` keep working but the dialog change makes the property feel like dead code | Very low | Document the property in `installer/build-msi.ps1` comments + `docs/cli-telemetry-command.md`'s silent-install section. |

---

## Decision asks

  1. **Approve the binary-consent direction?** Yes / No / Modify.
  2. **Approve the proposed labels** ("Don't share anything" / "Share
     anonymous bug + version counts")? Yes / suggest alternatives.
  3. **Approve the migration story** (preserve existing tier on
     upgrade; skip dialog when tier already set)? Yes / No / Modify.
  4. **Schedule**: target v1.0.1, or push to v1.0.2/v1.1?
  5. **Do you want a separate `--telemetry` CLI flag** to set tier
     during scripted/dev installs (e.g. `smirror selfupdate
     --telemetry standard`)? Out of scope of this proposal but worth
     deciding while we're here.

If 1-3 are all "yes" the implementation is ~50 LOC across WiX +
docs, plus the new system-validation tests. The work fits a single
focused commit cycle.

---

## Out of scope (deferrable)

  - **Three-radio → two-radio in winget manifest**: winget doesn't
    have a consent dialog; the user sees the privacy policy via the
    package description. No change.
  - **Three-radio → two-radio in `smirror task install` / `smirror
    service install`** flows: those are CLI-only and already have
    full three-tier surface. No change.
  - **Re-design of the three-tier CLI** itself: out of scope.
    Reliability remains a power-user CLI tier.
  - **Removal of Reliability from the architecture**: explicitly
    rejected. Reliability tier preserves the optionality for
    operator-class users; deleting it would be a real architectural
    regression.
  - **A/B testing the dialog wording** to measure consent rate
    deltas: out of scope for an OSS single-maintainer project. Pick
    the principled wording, document the choice, ship.

---

## Files of record

This proposal: `docs/PROPOSAL-2026-05-03-msi-binary-consent.md`.

Cross-references:
  - `installer/TelemetryConsent.wxi` (the file this proposal would
    edit).
  - `docs/PRIVACY.md` (the contract that the dialog surfaces).
  - `docs/cli-telemetry-command.md` (the CLI surface that keeps all
    three tiers).
  - `cmd/smirror/cmd_telemetry.go` (no code change required).

---

— v1.0.1+ design, awaiting maintainer approval.
