Run the initial project setup. Transform this template into a project-specific AI context system.

Read `INTERVIEW.md` first. Then read any other `.md` documents in the repo root that look like design docs, requirements, or architecture notes. Skip `CLAUDE.md`, `ROADMAP.md`, `CONTRIBUTING.md`, `README.md` — those are templates you will overwrite.

## Before generating anything

Check what you have from the interview:
- Stack defined? (language, framework, database)
- Primary users and interaction model clear?
- Phase and key constraints identified?
- At least one non-negotiable or architectural constraint?

If critical stack or scope information is missing, ask up to 5 focused clarifying questions in a single message. Wait for answers before writing any files.

## Generate in this order

### 1. CLAUDE.md

Replace all `[placeholder]` content with real project content. Keep every section heading and formatting instruction — only replace the placeholders.

- **Project Overview:** real name, one-line description, problem statement, non-goals
- **Stack:** actual language, framework, database, deployment target, key libraries (only non-obvious ones)
- **Architecture Notes:** 3–5 invariants specific to this project that Claude must never violate. Derive from the interview's non-negotiables, constraints, and design docs. Be specific — not "keep it simple" but "all mutations update authoritative intent only."
- **Current State:** phase from interview, active work blank, next priority = first ROADMAP item
- **Domain Reference Files table:** list only domain files you will actually create in this session
- **Settled Decisions:** anything from the non-negotiables or interview that qualifies as a settled decision

Do not leave any `[placeholder]` or `[e.g. ...]` text in the output.

### 2. Domain reference files

Based on the stack, create appropriate files in `docs/claude/` using `docs/claude/_TEMPLATE.md` as the structure. Only create files with real content — do not create empty or placeholder-only files.

Common patterns:
- Any app with login/sessions/tokens → `AUTH.md`
- Any app with a database or persistent store → `SCHEMA.md`
- Any app with an HTTP API → `API.md`
- Any app with a UI layer → `UI.md`
- Significant infra or deployment complexity → `INFRA.md`

Each domain file should contain the key decisions, conventions, and gotchas specific to that domain given the stack and interview answers. Leave gotchas blank rather than inventing them — they get filled in as the project develops.

### 3. ROADMAP.md

Replace the example items with real work items derived from the requirements:

- First item is always: project scaffold, local dev environment, CI (if CI is in scope)
- Order items by dependency — foundational items first
- Each item needs a "Done when" checklist with testable criteria
- Aim for 4–8 items for the immediate phase; put anything beyond that in Deferred
- Use realistic effort labels: `small` (hours), `medium` (1–2 days), `large` (week+)
- Set the Phase and Goal header to reflect the current milestone

### 4. CONTRIBUTING.md

Fill in the **Development setup** section with real commands for the actual stack:
- Install dependencies command
- Start local stack command
- Run tests command

Leave all other sections as-is.

## After writing all files

Output:
- List of files created or updated
- The first work item from the new ROADMAP.md
- Any open questions that need a human answer before development can start

Then say: "Review the generated files — these are your team's source of truth, edit freely. When ready:
```
git add CLAUDE.md ROADMAP.md CONTRIBUTING.md docs/
git commit -m 'chore: initial project setup via starter-repo interview

AI-model: opus
AI-settled: (list key settled decisions from CLAUDE.md)'
```"
