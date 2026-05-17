# UI Reference

> **When to load this file:** Read this before working on frontend, components, views, styles, or anything the user sees.

---

## Overview

[1–3 sentences: what the UI layer covers, how it's structured, and how it connects to the backend.]

---

## Key decisions

Settled decisions for this domain. Do not re-suggest these.

- **[UI framework]** — [e.g., React with Next.js App Router chosen for SSR and file-based routing. Do not suggest migrating to Pages Router.]
- **[State management]** — [e.g., Zustand for client state; no Redux. Server state via React Query.]
- **[Styling]** — [e.g., Tailwind only — no CSS modules, no styled-components.]
- **[Component library]** — [e.g., shadcn/ui — copy components into `/components/ui`, do not import from npm.]

---

## Conventions

- [e.g., All page components live in `app/` — no components defined directly in route files]
- [e.g., Shared components in `components/` — colocate with the page if used in one place only]
- [e.g., No inline styles — use Tailwind classes only]
- [e.g., File naming: `kebab-case` for files, `PascalCase` for component exports]

---

## Common patterns

[Non-obvious patterns specific to this project. Skip standard framework idioms.]

```tsx
// Example: how this project structures a page component
```

---

## Gotchas

- **[Gotcha]** — [what happens and how to avoid it]
- **[Gotcha]** — [what happens and how to avoid it]

---

## External references

- [Design system / Figma](url)
- [Component library docs](url)

---

## Domain file maintenance

Update this file when:
- A new UI framework or library decision is made
- A pattern changes and existing components need updating
- A gotcha is found in code review or user-reported bug

Updates must be in the same PR as the code change that prompted them.
