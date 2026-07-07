End the current session with a clean handoff.

Do the following in order:

1. **Update ROADMAP.md** — mark any completed items as `done`, update in-progress items. If new work was discovered during this session, add it as a new `WRK-xxx` item.

2. **Update CLAUDE.md `## Current State`** — update the active work and next priority to reflect where things stand right now.

3. **Update topic docs** — if any settled decisions, gotchas, or conventions were discovered during this session, add a bullet to the relevant `docs/reference/<DOMAIN>.md` `## Settled Decisions` section. Current rules only — no dated bullets, no rejected-alternatives, no separate ADR files. If no topic doc exists for the area, note the decision in `CLAUDE.md` Settled Decisions instead.

4. **Summarize for the developer** — output a short summary:
   - What was completed
   - What changed in CLAUDE.md or domain files (and why it mattered)
   - What to pick up next session
   - Any open questions or decisions that need a human answer before continuing

5. **Hand back to the developer.** Do not commit. Do not push. Leave
   committing to the developer.

Do NOT ask the developer to add `AI-model:` / `AI-settled:` commit trailers.
AI assistance is assumed across every commit in this project — a
constant-valued disclosure is information-free. Provenance (intent,
steering, verification, human ownership) lives in the PR description
under the "How it came together" section of the PR template, where it
varies per change and is what a reviewer actually needs.
