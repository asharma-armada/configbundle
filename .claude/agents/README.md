# Claude Agents

This directory is where specialized Claude sub-agents live. Agents are the natural evolution of single-agent sessions once a codebase and team reach a certain scale.

---

## When to introduce agents

Start with a single-agent workflow. Introduce agents when you hit real problems, not in anticipation of them:

- **Context window pressure** — a session regularly hits context limits before completing meaningful work
- **Specialist knowledge** — one domain (security, frontend, infra) has enough unique context that loading it in every session wastes tokens and degrades unrelated work
- **Parallel independent tasks** — multiple work streams that don't share state can run simultaneously
- **Repetitive review work** — the same checks happen on every PR (style, security, test coverage)

Do not introduce agents because they feel more "advanced." A well-maintained `CLAUDE.md` and domain files handle most projects through MVP.

---

## How agents work

An agent is a Markdown file in `.claude/agents/` that defines:
- **Role** — what this agent is responsible for
- **Context** — what it needs to load (domain files, tools, constraints)
- **Allowed tools** — scoped to what it actually needs (a reviewer agent probably shouldn't write files)
- **Interface** — how it receives tasks and what it returns

When a lead agent (or a developer) spawns a sub-agent, the sub-agent loads its own CLAUDE.md and agent definition, then executes in an isolated context window. Results are returned to the caller.

---

## Agent file format

```markdown
---
name: [agent-name]
description: [one sentence — used by Claude to decide when to use this agent]
model: sonnet | opus | haiku
tools: [comma-separated list of allowed tools]
---

## Role

[What this agent does and what it is responsible for. Be specific — a vague role produces a vague agent.]

## Context

Load before starting:
- @docs/reference/[DOMAIN].md
- [other files this agent needs]

## Constraints

- [What this agent must never do]
- [Scope limits — e.g. "only reads files, never writes"]

## Output format

[How this agent should return results — structured JSON, prose summary, list of findings, etc.]
```

---

## Example agents to consider as your project grows

These are starting points — define them when you have a real need, not before.

### `code-reviewer.md`
Runs on every PR. Checks for: adherence to conventions in CLAUDE.md, missing test coverage for "Done when" criteria, unintended scope creep, security anti-patterns.

**When to create:** when you find yourself asking Claude to review PRs regularly and the review prompt is getting long and repetitive.

### `security-reviewer.md`
Focused on: auth flows, input validation, secret handling, dependency vulnerabilities. Uses Opus — security decisions have long-term consequences.

**When to create:** before your first production deployment, or when Spike 8 (authorization) is active.

### `schema-validator.md`
Validates proposed schema changes against migration safety rules: backwards compatibility, nullable vs non-nullable, index requirements.

**When to create:** when schema changes are frequent and review misses are causing production incidents.

### `domain-specialist.md` (per domain)
Loads a specific domain file and handles implementation tasks in that domain only. Useful when a domain (e.g. auth, payments, ML pipeline) has enough context that loading it in a general session wastes too much of the context budget.

**When to create:** when a domain file exceeds ~200 lines and domain work is a significant fraction of total work.

---

## Hub-and-spoke pattern

For large parallelizable tasks, a lead (manager) agent coordinates specialist sub-agents:

```
Lead agent
├── Reads ROADMAP.md, decomposes WRK-xxx into sub-tasks
├── Spawns sub-agent A → handles database layer
├── Spawns sub-agent B → handles API layer  
└── Collects results, resolves conflicts, writes final output
```

The lead agent uses Opus. Sub-agents use Sonnet unless their domain warrants otherwise.

**Prerequisites before using hub-and-spoke:**
1. Each sub-agent's domain file is complete and accurate
2. The work items are genuinely independent (no shared mutable state)
3. You have clear "Done when" criteria for each sub-task

---

## Team governance

Agent definitions are code. They go through the same PR process as everything else. The CLAUDE.md sync rule applies: if an agent's behavior changes in a way that reflects a new architectural decision, update the relevant domain file in the same PR.

When a new developer joins, they read `CONTRIBUTING.md` (which references this file) before running any agents. Agents are powerful — a poorly scoped agent with write access can make large, hard-to-review changes quickly.
