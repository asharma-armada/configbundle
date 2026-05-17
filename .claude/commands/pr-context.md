Review the current diff against domain context files and surface what needs to be updated before this PR merges.

**Usage:** `/pr-context [domain]`
- With domain: `/pr-context api` — checks diff against `docs/claude/api-context.md` only
- Without domain: auto-detects relevant domain files from the diff

This is Step 3 of the Context Commit Protocol — the `terraform plan` equivalent. It shows what would change in context before you commit it.

---

## Step 1 — Get the diff

Run `git diff main...HEAD` to get all changes in this branch. If that produces nothing, try `git diff HEAD~1` for the last commit. Read the full diff.

---

## Step 2 — Identify relevant domain files

If a domain was specified: use that file only.

If no domain was specified: read `docs/claude/_index.md`, then match changed file paths against the routing table to identify which domain files are relevant. A PR touching `/src/api/` and `/src/db/` is relevant to both `api-context.md` and `data-context.md`.

Read every relevant domain file in full.

---

## Step 3 — Compare diff against domain files

For each relevant domain file, identify:

**New settled decisions** — choices made in this diff that aren't in the domain file.
Look for: new patterns introduced, existing patterns changed, constraints added or removed, library or dependency choices, error handling conventions, naming conventions, anything the team would want future Claude sessions to know.

**Outdated entries** — content in the domain file that this diff contradicts or supersedes.
Look for: old patterns the diff replaces, decisions this diff reverses, conventions this diff breaks intentionally.

**New gotchas** — failure modes or non-obvious behaviors introduced by this diff.

---

## Step 4 — Output the review

If nothing needs updating:
> Context is up to date. No updates needed for this PR.

If updates are needed, use this format:

```
─────────────────────────────────
Context Diff — [domain-file]
─────────────────────────────────
ADD to Key decisions:
  • [Decision] — [one-line rationale]

ADD to Conventions:
  • [Convention or rule]

ADD to Gotchas:
  • [Gotcha title] — [what happens and how to avoid it]

UPDATE (superseded):
  • Line: "[old content]"
    Replace with: "[new content]"
─────────────────────────────────
```

Only include sections that have entries. Skip empty sections.

If multiple domain files need updates, output a separate block for each.

---

## Step 5 — Prompt for action

After the diff output, say:

> "Review the proposed additions above. To apply them, say `apply context` and I'll update the domain file(s) in place. These updates should be committed in this PR — not deferred."

If the developer says `apply context`: make the edits to the domain file(s) directly. Do not ask for confirmation a second time.

---

## What this is not

- Not a code review — this is context hygiene only
- Not exhaustive — it surfaces candidates; the developer decides what's worth capturing
- Not a blocker — the developer chooses whether to apply the suggestions
