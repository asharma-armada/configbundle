# [Domain] Reference

> **When to load this file:** Read this before working on [brief description of the domain].
>
> Delete this callout block in your generated domain file.

---

## Overview

[1–3 sentences describing what this domain covers and how it fits into the broader system.]

---

## Key decisions

Settled decisions for this domain. Do not re-suggest these.

- **[Decision]** — [brief rationale]. See [ADR-xxx](../decisions/xxx-title.md) if the rationale warrants deeper explanation.
- **[Decision]** — [brief rationale].

---

## Conventions

[Naming conventions, code patterns, architectural constraints specific to this domain.]

- [Convention or rule]
- [Convention or rule]

---

## Common patterns

[Code patterns Claude should follow in this domain. Include only patterns that are non-obvious or project-specific — not standard language idioms.]

```[language]
// Example showing the correct pattern
```

---

## Gotchas

[Things that look right but are wrong, silent failure modes, or anything that burned the team at least once.]

- **[Gotcha title]** — [what happens and how to avoid it]
- **[Gotcha title]** — [what happens and how to avoid it]

---

## External references

[Links to relevant documentation, API references, or related domain files — only what Claude would realistically need to look up.]

- [Description](url-or-path)

---

## Domain file maintenance

This file should be updated whenever:
- A new settled decision is made in this domain
- A gotcha is discovered in code review or production
- A pattern changes and old code needs to be updated

Updates to this file must be included in the same PR as the code change that prompted them.
