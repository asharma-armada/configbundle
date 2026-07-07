Review the current diff against topic reference docs and surface what needs to be updated before this PR merges.

**Usage:** `/pr-context [domain]`
- With domain: `/pr-context API` — checks diff against `docs/reference/API.md` only
- Without domain: auto-detects relevant topic docs from the diff

This is Step 3 of the Context Commit Protocol — the `terraform plan` equivalent. It shows what would change in context before you commit it.

---

## Step 1 — Get the diff

Run `git diff main...HEAD` to get all changes in this branch. If that produces nothing, try `git diff HEAD~1` for the last commit. Read the full diff.

---

## Step 2 — Identify relevant topic docs

If a domain was specified: use that file only.

If no domain was specified: read the Reference Index in `CLAUDE.md`, then match changed file paths against the routing table to identify which topic docs are relevant. A PR touching `/internal/bundler/` and `/internal/controller/` is relevant to both `API.md` and `EDGE.md`.

Read every relevant topic doc in full.

---

## Step 3 — Compare diff against topic docs

For each relevant topic doc, identify:

**New settled decisions** — choices made in this diff that aren't in the topic doc's `## Settled Decisions` section.
Look for: new patterns introduced, existing patterns changed, constraints added or removed, library or dependency choices, error handling conventions, naming conventions, anything the team would want future Claude sessions to know.

**Outdated entries** — content in the topic doc that this diff contradicts or supersedes. UPDATE in place — don't add a superseding record.

**New gotchas** — failure modes or non-obvious behaviors introduced by this diff. Prefer "do NOT reintroduce X" landmine framing over background narrative.

---

## Step 4 — Output the review

If nothing needs updating:
> Context is up to date. No updates needed for this PR.

If updates are needed, use this format:

```
─────────────────────────────────
Context Diff — docs/reference/[TOPIC].md
─────────────────────────────────
ADD to Settled Decisions:
  • [Rule] — [one-line justification if non-obvious]

UPDATE (superseded — edit in place):
  • Line: "[old content]"
    Replace with: "[new content]"
─────────────────────────────────
```

Only include sections that have entries. Skip empty sections.

If multiple topic docs need updates, output a separate block for each.

---

## Step 5 — Prompt for action

After the diff output, say:

> "Review the proposed additions above. To apply them, say `apply context` and I'll update the topic doc(s) in place. These updates should be committed in this PR — not deferred."

If the developer says `apply context`: make the edits to the topic doc(s) directly. Do not ask for confirmation a second time.

---

## What this is not

- Not a code review — this is context hygiene only
- Not exhaustive — it surfaces candidates; the developer decides what's worth capturing
- Not a blocker — the developer chooses whether to apply the suggestions
