# SelectiveMirror Privacy Policy

**Audience**: end users of SelectiveMirror.
**Plain-language version of**: `docs/telemetry-microserver-architecture.md` (the technical spec).
**Last updated**: 2026-04-28.

---

SelectiveMirror is a free open-source tool. It synchronizes files between your machine and cloud backends. We try hard to be respectful of what we touch and what we send anywhere.

This document tells you in plain English: what we collect, what we don't, when, and how to opt out.

---

## TL;DR

| Channel | Default | Frequency | What it carries |
|---------|---------|-----------|-----------------|
| Bug reports (`smirror report-bug --submit`) | **Off** until you run it | Only when you explicitly run the command and approve the payload | Sanitized diagnostic bundle, anonymous install ID, smirror version |
| Install telemetry (opt-in via MSI checkbox or `smirror telemetry on`) | **Off** | Twice per install ever: once at first run, once per upgrade | Anonymous install ID, smirror version, OS family/version, CPU arch, install method, configured backends |
| Crash reports | **Local-only**, always | Only saved to your disk; never auto-uploaded | N/A (stays on your machine) |

If you do nothing, **nothing leaves your machine**. SelectiveMirror is silent by design.

---

## What we collect — in detail

### 1. Bug reports (per-event explicit approval)

When you run `smirror report-bug --submit`, smirror builds a sanitized diagnostic bundle and **shows it to you in full**. You see exactly what's about to be sent. You can edit it, redact your install ID, or cancel.

The bundle contains:
- Your smirror version
- Your OS family + version
- Your config structure (mirror count, delete policy, backend types — **not** paths or remote URLs; those are redacted)
- Your last 30 log lines (sanitized: home directory replaced with `~`, remote URLs replaced with `<REDACTED>`)
- Anomaly history from your local DB (only if you pass `--include-anomalies`)
- Crash data from your local crash files (only if you pass `--include-crashes`)
- Your anonymous install ID (a random UUID generated when you first ran smirror; you can redact it from the preview)

You will never submit a bug report without seeing the contents and approving them first. Per-event consent is the rule, not the exception.

### 2. Install telemetry (one-time opt-in, default off)

If you opt in (either at install time via the MSI checkbox or later via `smirror telemetry on`), smirror sends two events over the lifetime of an install:

- **first_seen**: when smirror runs for the first time on a fresh state DB. Carries: install ID, smirror version, OS family ("windows"), OS detail ("Windows 11 Pro 24H2"), CPU arch ("amd64"), install method ("msi" / "winget" / "zip" / "selfupdate"), configured backend types (e.g., `["gdrive", "s3"]` if any).
- **upgrade**: when smirror detects its version changed since last run. Same fields.

That's it. Two events per install per upgrade. No heartbeats. No daily check-ins. No usage metrics, no file paths, no sync counts, no bytes-uploaded numbers.

### 3. Crashes — never sent automatically

When smirror panics, it writes the stack trace and context to `~/.selectivemirror/crashes/`. These files **stay on your machine forever** (subject to local rotation). They are never auto-uploaded.

You can choose to attach them to a bug report by running `smirror report-bug --include-crashes`, in which case they're shown to you in the preview before submission.

---

## What we never collect

Under no circumstances does SelectiveMirror collect:

- **Your name, email, or any identity.** Not from your OS user account, not from your config.
- **File paths.** Source paths in your config show up as bare names; remote paths are fully redacted.
- **File contents.** Ever.
- **Filenames.** Ever.
- **URLs of your remotes** (the actual cloud paths). Only the backend type ("gdrive" vs "s3") is recorded.
- **Credentials.** rclone tokens, API keys, passwords — none of these reach the wire.
- **Hostnames** of your machine. Not collected.
- **MAC addresses, serial numbers, or hardware fingerprints.**
- **Your IP address.** The Cloudflare edge proxy may log the IP of incoming requests for rate-limiting purposes, but this is not stored beyond the rate-limit window (60 seconds) and is never written to the database.
- **Timestamps that could deanonymize you.** All timestamps in submissions are UTC; no local timezone is recorded.

---

## How to opt in / opt out

### Install telemetry

**Opt in via MSI**: When installing the MSI, check the "Send anonymous install events" checkbox.

**Opt in via CLI**:
```
smirror telemetry on
```

**Opt out via CLI** (any time, no admin needed):
```
smirror telemetry off
```

**Check current state**:
```
smirror telemetry status
```

### Bug reports

There is no global setting. Each `smirror report-bug --submit` invocation is its own decision. To skip submission, just don't pass `--submit`. To submit somewhere other than telemetry (e.g., a public GitHub issue), use `smirror report-bug --browser` (opens a prefilled GitHub issue page in your browser).

---

## Where the data lives

- **Backend**: Supabase PostgreSQL, hosted in West EU (Ireland). Account owned by Raveh Neeman personally.
- **Edge proxy**: Cloudflare Worker at `smirror-telemetry.selectivemirror.workers.dev`, free tier.
- **Retention**: raw payloads are stripped from the database after 90 days; aggregate counts are retained indefinitely.

The choice of EU region means data is subject to GDPR by default.

---

## Your rights

- **Withdraw consent at any time**: `smirror telemetry off` (immediate effect; queued events are deleted from disk).
- **Reset your install ID**: delete `~/.selectivemirror/state.db` and restart smirror. A fresh anonymous UUID will be generated on next run.
- **Request deletion of past data**: open a GitHub issue at https://github.com/qraveh/SelectiveMirror/issues with your install ID (the first 8 characters are enough). The maintainer will manually delete matching rows. Note: future data submitted under the same install ID will reappear unless you also reset it.

---

## How telemetry data is used

The maintainer (Raveh) reviews aggregated telemetry weekly via an automated digest committed to the repo at `docs/telemetry/weekly-*.md`. Key questions answered:

- Which versions are active in the wild? (helps prioritize backports, deprecations)
- Which bug signatures recur across multiple installs? (helps prioritize fixes)
- What install channels are used? (helps decide where to invest distribution effort)

The data is NOT used to:
- Track individual users
- Sell to third parties
- Send marketing
- Generate ML/AI training datasets
- Build behavioral profiles

---

## How to verify

- **Source code**: Open. https://github.com/qraveh/SelectiveMirror
- **Telemetry client code**: `internal/telemetry/` — read it.
- **Server schema**: `docs/telemetry-microserver.sql` — read it.
- **Running validation**: `test/telemetry-validation.py` — proves the security model.
- **Weekly digests**: `docs/telemetry/weekly-*.md` — see exactly what aggregates the maintainer reviews.

If you find any discrepancy between this policy and the actual behavior, it's a bug. Please file it.

---

## Contact

For privacy questions or data deletion requests:

- GitHub issue: https://github.com/qraveh/SelectiveMirror/issues
- Email: `smirror@qodeh.com`
- Security-sensitive issues: see `SECURITY.md`
