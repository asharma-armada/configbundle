Review the current changes against the project's conventions, architecture, and quality standards.

## 1. Determine scope

Run `git diff main...HEAD` to see all changes on this branch. If on main or if that returns nothing, run `git diff HEAD~1` to review the last commit.

Identify which `WRK-xxx` item(s) these changes correspond to. If you cannot determine this from the diff and branch name, ask before continuing.

## 2. Load context

Read:
- `CLAUDE.md` — architecture invariants, working style, settled decisions
- `ROADMAP.md` — the "Done when" criteria for the relevant WRK item(s)
- Any domain files in `docs/claude/` that are relevant to the changed files

## 3. Review against four standards

**Conventions**
Does the code follow the patterns and constraints in CLAUDE.md and the relevant domain files? Call out specific violations with file and line reference.

**Scope**
Does the change stay within the WRK item's defined scope? Flag any unrelated cleanup, refactoring, or additions bundled in.

**Done-when coverage**
Go through each criterion in the "Done when" checklist for the relevant WRK item. For each one, determine whether the changes in the diff address it.

**CLAUDE.md sync**
Were any architectural decisions made in this change — pattern choices, library selections, constraint discoveries — that are not yet reflected in CLAUDE.md or a domain file? If yes, these must be added before the PR can merge.

## 4. Output

```
## Review: [branch name or WRK-xxx title]

**WRK item:** [WRK-xxx — title]

### Conventions
[Findings, or "Follows established conventions."]

### Scope
[Findings, or "Change is scoped to WRK-xxx."]

### Done-when coverage
- [x] Criterion — addressed
- [ ] Criterion — not yet addressed (what is missing)

### CLAUDE.md sync
[Required / Not required — if required, list what to add and where]

### Verdict
[One of: Ready to merge / Needs work / Blocked on CLAUDE.md sync]

[2–3 sentences summarizing what is good, what needs attention, and what is blocking merge if anything]
```

If the diff is empty, say so and stop.
