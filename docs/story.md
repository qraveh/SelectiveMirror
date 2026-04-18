# Seven Days to Ship: How One Architect and an AI Built a Production File Sync Engine

*A story about SelectiveMirror, the tool that watches your files and mirrors them to the cloud — built in a week by a human-AI team that rewrote the rules of software development velocity.*

---

## The Problem Nobody Solved

In March 2026, Raveh Neeman — a hardware/software architect and quantum computing researcher based in Israel — was building something ambitious: an AI orchestration system that makes Claude, ChatGPT, Gemini, Copilot, and other AI systems collaborate on the same project. The central challenge wasn't the AI coordination itself. It was the storage.

Every AI tool in the orchestration needs access to the project's files. Not all files — not build artifacts, not `.git` internals, not multi-gigabyte datasets. Just the files worth tracking: source code, documentation, configuration, memory files that AIs use to maintain context across sessions.

Neeman needed a file mirror. Not Dropbox (syncs everything, no filtering). Not `rsync` (batch-only, no real-time). Not rclone alone (powerful backend support, but no watcher, no filtering, no intelligence). He needed something that watches directories in real-time, applies `.gitignore`-style filtering, handles the chaos of editors saving files ten times in five seconds, and pushes changes to any of 70+ cloud backends through rclone — all as a Windows service running silently in the background.

Nothing like this existed. So he built it. In seven days.

---

## The Numbers

SelectiveMirror, as it stands at v0.7.26-dev on April 2, 2026:

- **8,331 lines** of production Go code across **14 packages**
- **9,212 lines** of test code — more tests than production code
- **530+ unit tests** + 2 fuzz tests + 6 PowerShell integration test scripts
- **4,242 lines** of documentation (SRS, V&V Plan, user manual, developer manual, installation guide)
- **83 bug reports** filed in a structured BugTracker with full causation analysis
- **114 commits**, 4 tagged releases (v0.4.0 through v0.7.0)
- **24 Claude Code sessions** across 7 calendar days
- A **WiX MSI installer**, **GitHub Actions CI/CD**, **GoReleaser** automation, and a **Windows Event Log** integration

The total codebase — including tests, documentation, CI, installer, and configuration — exceeds **27,000 lines**.

---

## How Long Would This Take Manually?

This is the question that makes software engineers uncomfortable. Not because the answer is uncertain, but because it's so lopsided.

Let's be precise about what was built. This isn't a toy project or a weekend hack. SelectiveMirror is a **production Windows service** with:

- Real-time filesystem monitoring via Windows `ReadDirectoryChangesW`
- A fair scheduling queue with deduplication, priority deletes, and per-file cooldown
- A circuit breaker with exponential backoff per mirror
- Quiescence detection (waits for files to stabilize before syncing)
- An anomaly intelligence system with 11 event categories, JSON-lines recording, causal hypothesis templates, and file rotation
- A 4-category ghost taxonomy (LEAK, RETAINED, STALE, ORPHAN) that distinguishes intentional behavior from bugs
- Pre/post-sync hooks with environment variables and timeout
- Content-addressed sync skip using a trust model that separates local-wishful state from remote-verified state
- A gitignore-conformant filter engine (69-test conformance suite) using git's native wildmatch algorithm
- Named kernel event IPC for signaling the service without admin privileges
- Auto-migration framework for schema evolution
- SLA smoke testing, CI coverage gates, and structured release checklists

For a **top-10% developer** — someone who ships production code daily, knows Go well, understands Windows services, has worked with rclone before — here is a realistic estimate:

| Component | Manual estimate | AI-assisted actual |
|-----------|----------------|-------------------|
| Core sync engine + rclone integration | 2-3 weeks | 2 days |
| Filesystem watcher + debounce + FairQueue | 1-2 weeks | 1 day |
| Filter engine (gitignore compat) | 1 week | 4 hours |
| Windows service + installer | 1 week | 1 day |
| State DB + migrations | 3-5 days | 3 hours |
| Anomaly detection system | 1-2 weeks | 40 minutes |
| Hook system | 3-5 days | 15 minutes |
| Trust model (remote verification) | 1 week | 30 minutes |
| CI/CD + GoReleaser + MSI | 3-5 days | 1 day |
| Documentation (SRS, user manual, etc.) | 2-3 weeks | Concurrent |
| 530+ tests + conformance suite | 2-3 weeks | Concurrent |
| Bug finding + analysis + fixing | Ongoing | 83 bugs in 7 days |
| **Total** | **3-5 months** | **7 days** |

The ratio is roughly **15-20x**. But this assumes the best developers — the ones who already know Go, Windows services, rclone, and gitignore semantics.

### What about a typical developer?

A **top-50% developer** — competent, employed, ships code regularly, but without deep expertise in every domain this project touches — faces compounding slowdowns. Each knowledge gap doesn't just add time; it creates production incidents that consume time from the next feature.

The top-50% developer tries CGo-based SQLite first, hits cross-compilation issues, and switches to pure Go — losing 1-2 weeks. They get the Windows service running but miss that SYSTEM has a different PATH and rclone.conf location — two more weeks of debugging. They implement `.syncignore` filtering and ship it, not knowing that unanchored negation patterns match at any directory depth — that bug survives until a user reports 43 orphan files on Google Drive, months later.

The production hardening phase is where the gap becomes a chasm. Every edge case — Office applications saving through temp-file-rename cycles, fsnotify buffer overflows during burst events, Google Drive API rate limiting, duplicate directory IDs preventing ghost cleanup — is a surprise that triggers a debugging session. The top-10% developer anticipates half of these. The top-50% developer discovers each one through a production incident.

| | AI-assisted (actual) | Top 10% manual | Top 50% manual |
|---|---|---|---|
| Calendar time | **7 days** | 3-5 months | **8-14 months** |
| Feature completeness | Full | Full | ~60% (no anomaly system, hooks, trust model, content-addressed sync) |
| Test coverage | 530+ tests | ~200 tests | ~50 tests |
| Bugs found pre-production | 83 | ~20 | ~5 |
| Documentation | 4,242 lines | ~1,000 lines | README only |
| **Velocity ratio** | **1x** | **15-20x slower** | **40-60x slower** |

The features that distinguish a polished product from a working prototype — anomaly detection, causal hypothesis templates, content-addressed sync skip, the trust model separating local-wishful from remote-verified state — are the features a top-50% developer would never build. Not because they can't, but because the economics don't justify it for a solo project. AI collaboration changes that calculus: these features cost minutes instead of weeks, so they get built.

### The deeper advantage

The raw time comparison understates the real advantage. A human developer working alone faces a specific bottleneck that AI collaboration eliminates: **the feedback loop between writing code and understanding its consequences**. When SelectiveMirror's `.syncignore` filter had an unanchored negation pattern bug (SM-062), the AI traced the exact rclone filter generation, simulated the filter output, identified the excluded-parent constraint violation, and found that the fix needed to be in two places (the filter generator AND the pattern lint warnings) — all in a single analysis cycle. A human would discover the surface symptom, fix it, ship it, discover the deeper bug weeks later through user reports, and fix it again.

The 83 bug reports aren't a sign of poor quality. They're a sign that the AI found and fixed bugs that a human wouldn't discover until production. Bugs like: `deleteRemoteFile` silently discarding database errors (SM-079, critical — could cause incorrect remote deletions), or rclone stderr going to `/dev/null` when running as a Windows service (311 failures with zero diagnostic information). These are the bugs that live in production for months in manually-developed software.

---

## The Right Product at the Right Time

SelectiveMirror arrives at a specific moment in the evolution of AI-assisted development. As of early 2026, developers and researchers routinely work with multiple AI systems simultaneously — Claude for code generation, ChatGPT for architecture review, Copilot for inline suggestions, local models for sensitive data. Each system needs file access, and each has different capabilities and constraints.

The existing file sync landscape — Dropbox, OneDrive, Google Drive, rsync, rclone — was designed for human-to-human file sharing. These tools either sync everything (wasteful and insecure for AI contexts) or require manual batch operations (too slow for real-time AI collaboration).

SelectiveMirror occupies a specific niche that didn't exist two years ago: **selective, real-time, filtered file mirroring for AI workspaces**. Its `.syncignore` filtering means session transcripts, cached embeddings, and temporary AI artifacts never leave the local machine. Its anomaly detection means you know when something goes wrong without watching logs. Its content-addressed sync means your 15MB session transcript doesn't re-upload every time a metadata field changes.

The product's architecture — a single Go binary with zero runtime dependencies, backed by rclone's 70+ backend support — means it works with whatever storage your AI systems use: Google Drive for Claude's shared workspace, S3 for model artifacts, SFTP for on-premise research data, Backblaze B2 for archival.

Neeman chose to release it as free, open-source software under the MIT license. The reasoning is practical: the AI orchestration space is nascent, standards don't exist yet, and the tool that becomes the default file-sync layer for AI workspaces will be the one that's freely available, well-documented, and easy to integrate.

---

## What the Development Process Reveals

Perhaps more interesting than the product itself is what its development reveals about the state of AI-assisted software engineering in 2026.

The project was built across 24 Claude Code sessions. The longest single session — the one that shipped v0.4.0, v0.5.0, v0.6.0, and v0.7.0 in a single day — ran for approximately 12 hours, consumed 75% of a 1-million-token context window, and produced 40+ commits with 392 tests. During that session, the AI and human together:

- Discovered and fixed a critical filter engine bug that had caused 43 orphan files on Google Drive
- Migrated the gitignore library from an abandoned implementation to one using git's native wildmatch algorithm
- Built an entire anomaly intelligence system from scratch (classification, recording, sanitization, rotation, hypotheses)
- Implemented a pre/post-sync hook system
- Designed and implemented a named kernel event IPC mechanism for non-admin service signaling (after three failed approaches: SCM custom control, state DB polling, and named event with wrong DACL)
- Conducted a full library dependency audit and migrated two archived dependencies
- Filed 22 bug reports with complete causation analysis
- Created 7 development policies (release checklist, deprecation lifecycle, bug review process, mirror config tracking, versioning enforcement, library selection, taxonomy reporting)

The human's role was architectural direction, quality standards, and domain knowledge. The AI's role was implementation, testing, analysis, and documentation. Neither could have produced this result alone — a human working without AI would need months; the AI without a human would build the wrong thing.

This is not "AI replacing developers." This is a new development paradigm where the human provides judgment and the AI provides velocity. The result is software that would be economically infeasible to build as a solo project using traditional methods.

---

## What's Next

SelectiveMirror is approaching v1.0. The remaining work includes USN journal integration for fast restart reconciliation on Windows, telemetry (code written but not yet committed), and comprehensive load testing. Post-v1.0, the roadmap includes Go and Python programming APIs — extracting the filter engine and anomaly system as reusable libraries for the broader ecosystem.

The source code is at [github.com/qraveh/SelectiveMirror](https://github.com/qraveh/SelectiveMirror). The MSI installer, documentation, and contribution guidelines are included. It runs on Windows 10+ and supports every backend that rclone does.

As Neeman wrote in the only human-authored file in the entire project: "I am pleased to make it a free and open-source public utility."

The utility is ready. The age of AI-collaborative software development is here. And it took seven days.
