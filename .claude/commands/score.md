Assess the current session and produce an AI CaC health score.

Run this at any point: before starting work (pre-flight), mid-session, or before opening a PR. Same command, different value each time.

Do not summarize what you're about to do. Run the checks, then output the score.

---

## Step 1 — Assess context health

Use `git log --follow -1 --format="%cr" <file>` to determine freshness of each file.

**CLAUDE.md**
- Does it exist?
- When was it last updated?
- Is `## Current State` current (not blank or placeholder)?

**`docs/claude/_index.md`**
- Does it exist?
- Are the domain files it references actually present on disk?

**Each domain file listed in `_index.md`**
- Does it exist?
- When was it last updated?
- Is it substantively populated, or mostly template placeholders?

**Coverage gaps**
- Read the top-level source directory structure. Identify any major areas (e.g. `/src/auth`, `/src/billing`) that have no corresponding domain file in `docs/claude/`.

**AI commit trailers**
- Run `git log --grep="AI-model" -5` — are recent AI-assisted commits including `AI-model` trailers?

---

## Step 2 — Assess session efficiency

Reflect honestly on this session so far:

- Did you signal which domain files you loaded before starting the task?
- How many clarifying questions have you asked that a complete context file would have answered?
- Have any corrections been made to settled decisions already in CLAUDE.md or domain files?
- Have you read large portions of the codebase not specific to the task?

---

## Step 3 — Assess resource stewardship

- What model is currently active?
- Is that model appropriate for the current task type? Reference `## Model & Workflow Guide` in CLAUDE.md.
- Have any operations been broader than the task required?

---

## Step 4 — Output the score

Use this exact format. ✓ = pass, ⚠ = warning, ✗ = fail. One line per flag.

```
─────────────────────────────────
AI CaC Score
─────────────────────────────────
Context Health       [grade]
  [✓/⚠/✗] CLAUDE.md — [status]
  [✓/⚠/✗] [domain file] — [status]
  [✓/⚠/✗] Coverage — [gaps found, or "no gaps"]
  [✓/⚠/✗] AI commit trailers — [recent commits include AI-model trailer / missing]

Session Efficiency   [grade]
  [✓/⚠/✗] Context loaded and signaled: [files listed, or "not signaled"]
  [✓/⚠/✗] Clarifying questions: [count]
  [✓/⚠/✗] Corrections to settled decisions: [count]
  [✓/⚠/✗] Broad scans: [none, or describe]

Resource Stewardship [grade]
  [✓/⚠/✗] Model: [active model] — [appropriate / over-powered / under-powered]
  [✓/⚠/✗] [any other resource flag, or "No issues"]

Overall              [grade]
─────────────────────────────────
```

**Grading:**
- **A** — fully follows the practice, no flags
- **B** — minor warnings, nothing blocking
- **C** — one or more issues worth addressing before the PR
- **D** — significant practice gaps; address before continuing

Overall grade = lowest of the three dimensions.

---

## Step 5 — Suggestions

For every ✗ or ⚠ flag, generate a specific suggestion. Use tiered language:

- **✗ flags → "Fix before continuing:"** — active harm to the session
- **⚠ flags → "Worth addressing:"** — before the PR or next session

Each suggestion must include three things: **what to do**, **how to do it** (command if one exists), and **why it matters**.

**Suggestion templates by flag type:**

*Stale domain file (⚠ if 4–8 weeks, ✗ if 8+ weeks):*
> Worth addressing: `[file]` is [age] old. Run `/distill [domain]` pointed at recent PRs or ADRs to refresh it. A domain file drifting past 4 weeks in an active codebase means Claude is operating on stale decisions — the exact drift problem AI CaC exists to prevent.

*Missing domain file for a major code area (✗):*
> Fix before continuing: `/src/[area]` has no context file. Run `/distill [area]` to create one. Even a thin file is better than Claude operating blind in that area.

*Context not loaded or not signaled (⚠):*
> Worth addressing: Domain files weren't signaled at task start. Before continuing, state which domain files are relevant and read them. This ensures the rest of the session operates from current context, not assumptions.

*Clarifying questions that context should have answered (⚠):*
> Worth addressing: [N] questions asked that context should have covered. Before closing this session, add the answers to the relevant domain file. That's the compounding promise — the next session starts from a better baseline.

*Corrections to settled decisions (✗):*
> Fix before continuing: A settled decision was re-opened. Check whether CLAUDE.md or the relevant domain file needs updating to reflect the current decision. If the decision changed, update it now — in the same session, not as a follow-up.

*Wrong model for task type (⚠):*
> Worth addressing: [Model] is active for an [implementation/design] task. Run `/effort normal` to switch to Sonnet for implementation, or `/effort max` for architecture decisions. See the model guidance table in CLAUDE.md — four developers making independent model calls is the resource stewardship problem.

*Broad codebase scan (⚠):*
> Worth addressing: A broad scan was run where a targeted read would have sufficed. Next time, load the relevant domain file first — it often eliminates the need to scan. Targeted context is the point of domain files.

*CLAUDE.md missing or never updated (✗):*
> Fix before continuing: CLAUDE.md is missing or has never been updated from the template. Run `/setup` to generate a project-specific version. Without it, every session starts cold.

---

If all flags are ✓, output:
> All good. No actions needed.

Do not output suggestions for passing flags. Do not exceed 4 suggestions total — prioritize ✗ over ⚠.

---

## What this is not

- Not a judgment of code quality
- Not an exact cost report (token counts are not available)
- Not a blocker — the developer decides whether to act

This is a mirror. It shows the practice as it's being lived, not as it's intended.
