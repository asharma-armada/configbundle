# ADR-001: Domain context routing via `_index.md`, not per-domain skills

**Date:** 2026-05-18
**Status:** accepted

---

## Context

As a codebase grows, a single `CLAUDE.md` becomes too broad to be useful. Domain context files (`ui-context.md`, `api-context.md`, etc.) solve this by decomposing context by concern. This creates a routing problem: how does Claude know which domain file to load for a given task?

Three options were evaluated:

1. **Per-domain skills** — a `/ui`, `/api`, `/cmdb` skill for each domain that loads the relevant file before starting
2. **Central routing table** (`_index.md`) — one file that maps task types to domain files; Claude reads it and loads accordingly
3. **Claude infers from task description** — no explicit routing; Claude decides what to load based on the task

---

## Decision

`docs/claude/_index.md` is the single routing mechanism. Per-domain skills are not created. Claude inference is not relied upon for loading domain context.

---

## Rationale

**Against per-domain skills:** Every new domain requires a new skill. The skill library grows in parallel with the domain file library — two things to maintain instead of one. More importantly, the skill *is* the routing table, just split across N files. `_index.md` centralizes this as a single, reviewable, diff-able artifact. When the routing changes, there is one place to update.

**Against Claude inference:** Inferring which domain file to load from the task description is non-deterministic. Two developers describing the same task differently could result in different context being loaded. Context loading should be predictable, not probabilistic.

**For `_index.md`:** One routing table, owned by the team, reviewed in PRs. Adding a new domain is one row. Changing a domain mapping is one line. The routing is visible to anyone reading the repo — no tribal knowledge required.

---

## Consequences

**Positive:**
- Routing is transparent, reviewable, and maintained in one place
- Adding a domain is a single-row change to `_index.md` and `CLAUDE.md`
- No skill proliferation as the domain count grows

**Negative:**
- Requires Claude to read `_index.md` at task start — one additional file read per session
- Less ergonomic than typing `/cmdb` to explicitly load CMDB context; developer must describe the task clearly enough for Claude to route correctly

**Risks:**
- If `_index.md` descriptions are too generic, routing ambiguity returns. Row descriptions must be specific enough to match the task.
- Revisit if teams report that manual routing is a consistent source of context-loading errors. At that point, per-domain skills as an optional ergonomic layer (wrapping `_index.md` routing, not replacing it) may be warranted.

---

## Notes

The `/distill` command creates the domain file and updates `_index.md` in one operation. New domains go through `/distill`, not through manual file creation.
