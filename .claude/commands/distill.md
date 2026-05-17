Distill source documents into a domain context file.

**Usage:** `/distill [domain-name] [file paths or description of documents to read]`

This is a one-time setup operation. After the context file is created, it is maintained through the sync rule — not by re-running this command. The source documents become the historical record. The generated file becomes the living truth.

---

## Before reading anything

Confirm two things:
1. The domain name is specific and bounded (e.g., `cmdb`, `auth`, `billing` — not `backend` or `general`)
2. You have a clear list of source documents to read

If either is ambiguous, ask before proceeding.

---

## Read all source documents

Read every document provided in full. While reading, extract:

- **Settled decisions** — architectural choices, resolved constraints, things the team has already decided
- **Conventions and patterns** — how things are named, structured, or organized in this domain
- **Gotchas** — known failure modes, things that look right but are wrong, anything that has burned the team
- **External references** — URLs, specs, or related files explicitly mentioned

Do not invent content. If something is ambiguous in the source docs, note it as an open question rather than committing a guess to context.

---

## Generate the domain context file

Create `docs/claude/[domain-name]-context.md` using `docs/claude/_TEMPLATE.md` as the structure.

**Rules:**
- Replace every `[placeholder]` with real content derived from the source documents
- **Key decisions:** settled choices only; frame each as something Claude should not re-open
- **Conventions:** project-specific patterns only; omit standard language or framework idioms
- **Gotchas:** only include what is explicitly mentioned or clearly implied in the source docs
- **External references:** only URLs or file paths that appear in the source docs

---

## Update the routing table

Add a row to `docs/claude/_index.md`:

```
| Working on [one-line domain description] | [`[domain-name]-context.md`]([domain-name]-context.md) |
```

Add the same row to the Domain Reference Files table in `CLAUDE.md`.

---

## After writing files

Output:
- Files created and updated
- 3–5 bullet summary of the key decisions captured
- Any gaps — information that seemed important but was too ambiguous to commit as a settled decision

Then say:

> "Review `docs/claude/[domain-name]-context.md` before committing — edit freely. The source documents are now the historical record; this file is the living truth. Maintain it through the sync rule: any future decision in this domain belongs in this file, in the same PR as the code that motivated it."
