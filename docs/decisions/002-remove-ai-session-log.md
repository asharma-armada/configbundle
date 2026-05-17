# ADR-002: Remove AI.md session log; extend git commit conventions instead

**Date:** 2026-05-24
**Status:** accepted

---

## Context

The starter shipped with `AI.md` — an append-only session log recording model used, work items touched, decisions made, and a session summary. The intent was to make AI sessions auditable and give teams visibility into what Claude had been doing.

In practice, the file was never read. The information it captured fell into two categories:

1. **Decisions** — these belong in `CLAUDE.md` and domain files via the sync rule. If the sync rule is working, `AI.md` is redundant. If the sync rule isn't working, `AI.md` doesn't save you — nobody reads it.

2. **Session metadata** — model used, date, summary. Useful for auditing and resource stewardship, but lightweight enough to live in the git commit message rather than a separate file.

`AI.md` also introduced a second maintenance surface: the sync rule requires updating context files in PRs; `AI.md` required *another* update at session close. Two places to write the same information. One gets neglected. It's always the one with lower stakes.

The core principle: git is already the audit trail. The practice should extend on top of git, not introduce a parallel system.

---

## Decision

Remove `AI.md`. Extend commit message conventions to carry AI session metadata as git trailers.

---

## Rationale

**Git is already the audit trail.** Every AI-assisted commit already has a timestamp, author, and diff. Adding structured trailers makes AI context visible in the existing tool developers already use.

**Git trailers are greppable.** `git log --grep="AI-model: opus"` surfaces every session where a high-stakes model was used. `git log --grep="AI-settled"` surfaces every session that produced a settled decision. No separate file, no separate tooling.

**Zero additional maintenance burden.** The commit message is written at commit time — the moment when the work is freshest. There's no separate `AI.md` update step to forget.

**Extends convention, doesn't invent one.** The Conventional Commits format is already in `CONTRIBUTING.md`. Git trailers are a standard extension of that format.

---

## The new convention

AI-assisted commits include trailers in the commit body:

```
feat: add JWT validation middleware

Implemented RS256 token validation. All routes now require a valid token
from the auth service. Folio verifies tokens only — never issues them.

AI-model: sonnet
AI-settled: JWT tokens verified only, never issued by this service
```

**`AI-model`** — required on AI-assisted commits. Value: `sonnet`, `opus`, or `haiku`. Enables resource stewardship auditing.

**`AI-settled`** — optional. Include when the session produced a new settled decision. Brief description only — the full decision belongs in `CLAUDE.md` or the relevant domain file.

---

## Consequences

**Positive:**
- Session metadata lives in git where developers already look
- Greppable without any tooling beyond `git log`
- No separate maintenance step at session close
- `/wrap-up` is simpler — one fewer file to update

**Negative:**
- The IaC analogy table loses its "State file → `AI.md`" row; requires updating `README.md`
- Teams who never read `AI.md` lose nothing; teams who did read it (rare) lose the narrative format

**Risks:**
- Commit trailers are easily forgotten — `/wrap-up` should remind the developer to include them
- Without enforcement, some AI-assisted commits will have no trailers; this is acceptable

---

## Notes

The `AI-model` trailer was proposed as a lighter-weight replacement for `AI.md`'s model field. The `AI-settled` trailer replaces the "Decisions made" field. The session summary (2–4 sentences) maps to the commit message body — which already exists.
