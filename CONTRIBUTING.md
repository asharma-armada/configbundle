# Contributing

This document covers development conventions, AI workflow, and the PR process for this project. All contributors — human and AI-assisted — follow these standards.

---

## Development setup

1. Install Go 1.22+ and kubebuilder: `brew install go kubebuilder`
2. Install module dependencies: `go mod download`
3. Start the local Orbital stack (MinIO OCI registry at `localhost:5000`, orb at `localhost:8001`): `make up && make run-orbital` (run in the orbital repo)
4. Run the bundler locally: `go run ./cmd/bundler` (listens on `:8020`)
5. Run tests: `go test ./...`
6. Regenerate CRD manifests after type changes: `make generate && make manifests`

For the full local end-to-end test flow (export → publish with enricher → pull artifact), see [`configbundle-integration.md`](configbundle-integration.md#local-end-to-end-test-flow).

---

## Work item workflow

All work is tracked in `ROADMAP.md`. It is the source of truth for what is open, in progress, and done.

1. **Pick up work** — choose an `open` item from `ROADMAP.md`, set it to `in-progress`, open a branch named after it (e.g. `wrk-003-authentication`)
2. **Work the item** — implement against the "Done when" criteria
3. **Open a PR** — include ROADMAP.md update (mark item `done`) and CLAUDE.md update if any architectural decisions were made
4. **Review and merge** — at least one human approves before merge

No work that isn't in ROADMAP.md. If you're doing something, add the item first.

---

## AI workflow conventions

### Model selection

**Default: Sonnet** for all implementation work.

**Switch to Opus (`/effort max`) for:**
- Designing a new subsystem with long-term architectural impact
- Security-sensitive design (auth, signing, permissions, key management)
- Planning a new spike for the first time
- Reviewing a completed spike against architectural invariants
- Any task touching 3+ domains simultaneously

**Switch back to Sonnet (`/effort normal`)** once design is settled and the task is implementation of a known plan.

### Conversation prefixes

These gate how Claude responds — use them consistently:

- **`thoughts:` / `discuss:`** — exploratory dialogue. Claude responds conversationally only; no code written, no files edited.
- **`propose:`** — written design proposal for review before any implementation. Claude writes a proposal; no code.
- No prefix — Claude implements.

### Session hygiene

- Start each session by reading `ROADMAP.md` to orient on current state.
- Work one `WRK-xxx` item per session where possible. Unrelated tasks in the same session pollute context.
- Use `/clear` between unrelated tasks within a session.
- At the end of a session, run `/wrap-up` to update `CLAUDE.md`, save any architectural decisions, and update ROADMAP.md status.
- Commit before ending the session. Don't leave in-progress Claude work uncommitted.

### CLAUDE.md sync rule

Any session that produces a new architectural decision, settled constraint, or domain convention **must** include the corresponding `CLAUDE.md` or domain file update in the same PR as the code. The PR reviewer checks this.

This is how the team's AI context compounds in quality over time instead of drifting.

### Context Commit Protocol

The sync rule is enforced through a five-step workflow at PR time:

**1. Context Load** — at task start, Claude reads `docs/claude/_index.md`, loads relevant domain files, and signals what it loaded. *"Reading api-context.md before starting."* Do not proceed without this signal.

**2. Decision Surface** — during work, Claude flags decisions not covered in the domain files. *"This pattern isn't in api-context.md — flagging as a candidate for context commit."* These candidates are tracked through the session.

**3. Context Diff** — before opening the PR, run `/pr-context [domain]`. Claude reads the diff against the domain file and surfaces proposed additions. Review and approve or reject each one.

**4. Context Commit** — approved additions are written to the domain file and committed in the same PR as the code. Not in a follow-up. Not a separate ticket. Same PR.

**5. Context Review** — reviewers check for context updates the same way they check for tests. If a settled decision was made, the domain file update should be in the diff. If it isn't, ask for it before approving.

Run `/score` at any point to check the health of the current session against these practices.

### CLAUDE.local.md

Each developer can create a `CLAUDE.local.md` in the project root for personal preferences that don't belong in the shared `CLAUDE.md` (preferred libraries, personal style, things to never suggest). This file is gitignored and never committed.

---

## Commit conventions

Follow [Conventional Commits](https://www.conventionalcommits.org/):

```
feat: add JWT validation middleware
fix: correct token expiry calculation
chore: update dependencies
docs: add auth domain reference file
refactor: extract token parsing into separate function
test: add integration tests for auth flow
```

Keep commits atomic — one logical change per commit. If Claude does the work, the commit message is still your responsibility.

### AI-assisted commits

Git is the audit trail. AI session metadata belongs in the commit, not in a separate file.

Add trailers to the commit body on AI-assisted commits:

```
feat: add JWT validation middleware

Implemented RS256 token validation in middleware layer.

AI-model: sonnet
AI-settled: JWT tokens verified only, never issued by this service
```

**`AI-model`** — required on AI-assisted commits. Value: `sonnet`, `opus`, or `haiku`. Enables resource stewardship auditing: `git log --grep="AI-model: opus"` surfaces every high-stakes model session.

**`AI-settled`** — optional. Include when the session produced a new settled decision. One line only — the full decision belongs in `CLAUDE.md` or the relevant domain file.

The `/wrap-up` command will remind you to include these trailers before committing.

---

## PR process

**PR title:** short and descriptive (under 70 characters). Use the same prefix as conventional commits.

**PR body must include:**
- Summary of what changed and why (not what the code does — why this change)
- Test plan (what to verify, how)
- ROADMAP.md status updated? (yes/no)
- CLAUDE.md updated? (yes/no — required if architectural decisions were made)

**Review checklist:**
- [ ] Code follows project conventions in CLAUDE.md
- [ ] Tests cover the "Done when" criteria from ROADMAP.md
- [ ] CLAUDE.md and domain files updated if new decisions were made
- [ ] No unrelated changes bundled in

**Merge:** squash merge to keep history clean.

---

## Architecture Decision Records (ADRs)

Significant, long-lasting architectural decisions go in `docs/decisions/`. Use `docs/decisions/000-template.md` as the template.

An ADR is warranted when:
- The decision will be hard to reverse
- Multiple reasonable options existed and the reasoning behind the choice will not be obvious from the code
- You want future maintainers (human or AI) to understand why, not just what

Decisions that are routine, obvious, or easily reversed do not need an ADR.

---

## Agents (multi-agent patterns)

See `.claude/agents/README.md` for guidance on using specialized Claude agents. Agents are an advanced pattern — start with single-agent sessions and grow into agents when you hit real scaling limits.
