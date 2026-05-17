End the current session with a clean handoff.

Do the following in order:

1. **Update ROADMAP.md** — mark any completed items as `done`, update in-progress items. If new work was discovered during this session, add it as a new `WRK-xxx` item.

2. **Update CLAUDE.md `## Current State`** — update the active work and next priority to reflect where things stand right now.

3. **Update domain files** — if any settled decisions, gotchas, or conventions were discovered during this session, add them to the relevant `docs/claude/*.md` file. If no domain file exists for the area, note the decision in `CLAUDE.md` Settled Decisions instead.

4. **Summarize for the developer** — output a short summary:
   - What was completed
   - What changed in CLAUDE.md or domain files (and why it mattered)
   - What to pick up next session
   - Any open questions or decisions that need a human answer before continuing

5. **Remind the developer to commit with trailers** — end with:

> "When you commit, include AI trailers in the commit body:
> ```
> AI-model: [sonnet/opus/haiku]
> AI-settled: [brief description of any settled decision, if applicable]
> ```
> Git is the audit trail."

Do not commit. Do not push. Leave committing to the developer.
