# Resolution — Pre/post-sync hooks deferred from v1.0

**Date**: 2026-04-29
**Source version at time of decision**: 0.9.46-dev (HEAD `0e5a53a`)
**To**: SelectiveMirror development session
**From**: Project owner (Raveh) following a multi-role panel review
**Status**: ACCEPTED. Hooks moved to "watched / possible future" — explicitly **not** part of the v1.0 surface.

---

## 1. What is resolved

The pre/post-sync hooks subsystem (`internal/hooks/`, `pre_sync_hook` / `post_sync_hook` config keys, FR-ASP-17, Phase 7) is **not** committed as a v1.0 capability. It remains in the codebase for now in its current form, but:

- It is no longer treated as a stability promise.
- It is no longer treated as an extensibility seam the project markets to users.
- It is recorded in the deferred-features watch list and reconsidered only on explicit external demand.

Phase 7 in `CLAUDE.md` is no longer counted toward v1.0 readiness.

## 2. Why this is the right moment

The defender's strongest argument against any change to hooks — that removal or relabeling would be a breaking change — does not apply: **SelectiveMirror has never had a public release.** The latest published tag (v0.9.26, 2026-04-29) was produced for the project's own validation cycle, not distributed to outside users. There is no installed base, no winget channel surface, no GitHub release attached to a populated download counter. Hooks therefore have **zero known users** — internal or external.

Pre-1.0 is exactly the window in which "feature with no consumer" should be re-evaluated against its maintenance cost. Carrying it into 1.0 would convert that cost into a permanent compatibility commitment.

## 3. Review record (one-paragraph recap)

A multi-role panel review examined hooks against the two questions: (a) does the implementation deliver the marketed value, and (b) could the value be delivered without hooks. Findings of record:

- **"Validation" use case is structurally false** — hook errors are discarded at [internal/sync/sync.go:291](../internal/sync/sync.go) (`_ = e.Hooks.Run(...)`); a failing `pre_sync_hook` cannot block a sync.
- **Batch-sync gap (still open at review time)** — hooks fire only on the live-watcher path; `sync-now`, startup reconciliation, `dry-run`, and delete events all bypass them.
- **Use cases overlap with existing features** — `alert_webhook_url` covers incident notification; `sync_log` covers audit; `.syncignore` covers gating; remote-side event APIs cover downstream triggers; git `pre-commit` covers authoring-time validation.
- **Cost** — ~700 LOC across `internal/hooks/`, tests, config, sync integration, docs (`user-manual.md` §12, `SECURITY.md` hook section, `config.example.yaml`), plus recurring review attention.

The review majority recommended deprecation (path **C**); minimum acceptable interim was relabeling + truth-in-advertising (path **A**). The "fix to match docs" path (**B**) was rejected as the worst of both worlds — more code chasing a use case no one has demanded.

## 4. Decision (concrete)

The project adopts **path C in slow-motion**: deferral, not immediate removal.

| Item | State after this resolution |
|---|---|
| `pre_sync_hook` / `post_sync_hook` config keys | Remain accepted by `config.Validate()` for now. No removal in 0.9.x. |
| `internal/hooks/` package | Remains in tree. No new feature work, no extension to batch / delete events. |
| Batch-firing / delete-firing gap | **Closed as won't-fix** under the new framing — the gap was reported against the assumption that hooks should be load-bearing. They are not. |
| FR-ASP-17 in SRS | Reclassified from baselined v1.0 requirement to **"deferred / not part of v1.0"**. Wording change tracked as a follow-up commit, not part of this resolution. |
| Phase 7 in `CLAUDE.md` | Reclassified from "complete" to a deferred-feature footnote. |
| `docs/user-manual.md` §12 (Hooks) | To be revised: strike "validation / lint before upload" as a use case; strike "transformation"; mark the chapter as **experimental, not part of the v1.0 stability surface**. Truth-in-advertising change. |
| `docs/release-maturity.md` "Open Highs" row | The batch-sync hooks finding removed from the High count once §6 follow-up commit lands. |
| `SECURITY.md` hook section | Retained as-is (the hardening is correct for what it does). |
| `alert_webhook_url`, `sync_log`, `.syncignore` | Reaffirmed as the **supported** paths for notification, audit, and gating respectively. |

## 5. What this resolution does NOT do (kept for separate, atomic commits)

To keep this artifact reviewable, the following are **out of scope** and tracked as follow-up work:

1. Edit `docs/SRS.md` to reclassify FR-ASP-17 (must keep traceability ID; add "DEFERRED — see RESOLUTION-2026-04-29-hooks-deferred.md").
2. Edit `CLAUDE.md` Phases section to demote Phase 7 from `[x]` to a footnote.
3. Edit `docs/user-manual.md` §12 for truth-in-advertising.
4. Update `docs/release-maturity.md` Open-Highs row when the batch-sync hooks finding is closed.
5. Update the corresponding system-validation test cases — the `Hooks_*` test cases either become assertions of the documented limited behavior, or are skipped with a reference to this resolution.
6. CHANGELOG entry under the next `-dev` patch noting the deferral.
7. File a tracker ticket (SM-NNN, number to be assigned by maintainer) titled "Hooks deferred from v1.0 — possible future feature" linking to this document.

No code in `internal/hooks/` is removed by this resolution. Removal — if it ever happens — is a separate decision that requires its own resolution.

## 6. Re-opening conditions

This resolution is reconsidered, and hooks may be promoted back into the v1.0 surface, if **any** of the following occurs:

- A real user (issue, email, public-channel discussion) requests hooks for a use case `alert_webhook_url` + `sync_log` + `.syncignore` + remote-side events provably cannot meet.
- An in-tree consumer of hooks emerges (e.g., a future SelectiveMirror feature that itself shells out via the hook seam).
- A security finding raises the cost of *keeping* hooks above the cost of removing them, in which case the decision flips toward removal rather than promotion.

Absent any of those, hooks remain in deferred state for the entirety of the v1.0 cycle.

## 7. Signatures of record

- **Multi-role panel review (2026-04-29)**: majority verdict path C, dissent acknowledged from the maintainer perspective. Full transcript retained in this session's review discussion (not separately committed; this letter is the durable record).
- **Project owner (Raveh)**: accepted. Decision is recorded.

---

*This letter is the canonical reference for the hooks deferral. Future review rounds, SRS edits, and CHANGELOG entries should cite it by filename rather than re-litigating the verdict.*
