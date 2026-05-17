# Project Interview

Fill this out before starting your first Claude session. Leave fields blank if unknown — Claude will ask follow-up questions during the setup session. The more context you provide here, the less back-and-forth in the session.

---

## 1. Project identity

**Name:**

**One-line description:**

**Problem it solves:**

**Who requested or owns this:**

---

## 2. Users and usage

**Primary users** (operators, developers, end-users, automated systems):

**How they interact with it** (web app / CLI / REST API / SDK / library / background service / other):

**Scale expectations** (single tenant / multi-tenant / approximate user count / request volume):

---

## 3. Stack

Leave blank for any unknowns — Claude will surface options during the session.

**Primary language(s):**

**Framework(s):**

**Databases / stores:**

**Messaging / queues:**

**Deployment target** (Kubernetes / serverless / VMs / desktop app / embedded / other):

**Cloud provider** (if any):

**External integrations** (auth providers, third-party APIs, internal services):

---

## 4. Team

**Team size** (total developers who will touch this codebase):

**Will multiple developers use Claude on this codebase?** (yes / no):

**Approximate experience level of the team** (junior / mid / senior / mixed):

**Relevant existing standards** (link or paste any team coding standards, style guides, or architecture guidelines):

---

## 5. Phase and constraints

**Current phase** (spike / prototype / MVP / v1 / production / maintenance):

**Target delivery** (date, quarter, or "no hard deadline"):

**Biggest current constraint** (time / correctness / security / compliance / scalability / cost / other):

**Air-gap or offline requirements?** (yes / no / partial):

---

## 6. Design and requirements documents

List any documents already in this repo or that you plan to paste in. Claude will read these during setup.

- 
- 

---

## 7. Non-negotiables

Technology choices, architectural constraints, or decisions already made that should never be re-suggested.

- 
- 

---

## 8. What does "done" look like for this project?

Describe the end state — a deployed product, a published library, a demo, a handoff. This shapes how Claude frames work and sets priorities.

---

## 9. Anything else

Risks, known unknowns, political constraints, things that went wrong on a previous similar project, anything else Claude should know before starting.

---

## Setup instructions

Once you have filled this out:

1. Paste any design docs or requirements into the repo.
2. Open a Claude Code session in this directory.
3. Say: `Setup this project using INTERVIEW.md and any docs in this repo.`
4. Claude will ask follow-up questions, then generate:
   - `CLAUDE.md` — your project's AI context file
   - `docs/claude/` — domain reference files relevant to your stack
   - `ROADMAP.md` — work item list seeded from your requirements
   - `CONTRIBUTING.md` — team and AI workflow conventions
5. Review all generated files before committing. These are your team's source of truth — edit freely.
