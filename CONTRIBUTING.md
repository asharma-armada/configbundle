# Contributing

> **New here?** Start with [README.md](./README.md) — Quick Start walks you
> from a fresh clone to running end-to-end in 5 minutes. This doc covers
> everything you need *after* you're running: PR workflow, tests, releases,
> code style, and how the team uses Claude.

## Make commands at a glance

Run `make help` for the full list. The most-used:

| Daily | Tests | Release |
|---|---|---|
| `make up` / `make down` | `make test` (unit + envtest, ~30s) | `make push-controller` (cb-controller) |
| `make run-controller` | `make test-e2e-local` (against running controller) | `make push-bundler` (cb-bundler) |
| `make run-bundler` | `make test-e2e` (full CI, Kind cluster) | `make push-serverconfig` (sc-controller) |
| `make run-serverconfig` | | `make push-backupconfig` (bc-controller) |
| `make run-backupconfig` | | `make push-all` (all 4) |
| `make generate manifests` | | |

`make run-*` targets use `go run` for fast iteration. Metrics/health/dispatch
ports are documented in the [README's Quick Start](./README.md#quick-start).

## Work item workflow

All work is tracked in `ROADMAP.md`. Pick an `open` item, set it `in-progress`,
open a branch named after it (e.g. `wrk-014-observability-shape`), implement
against the "Done when" criteria, open a PR with a ROADMAP.md update marking
the item done.

**No work that isn't in ROADMAP.md.** If you're doing something, add it first.

## Running tests

```bash
make test              # unit + envtest — no cluster required, ~30s
make test-e2e-local    # requires `make run-controller` running
make test-e2e          # full CI path: builds image, Kind cluster, kustomize deploy
```

`make test` covers the reconciliation logic across all four controllers.
See individual `*_test.go` files for coverage breakdown:

- **CRD decomposition** — `internal/controller/configbundle_controller_test.go`
- **ConsumeServer apply pipeline** — `internal/controller/consume_test.go`
- **Divergence reporter** — `internal/controller/divergence_reporter_test.go`
- **Reclaim controller** — `internal/controller/reclaim_controller_test.go`
- **Takeover** — `internal/controller/takeover_test.go`
- **Bundler mapping** — `internal/bundler/handler_test.go`
- **sc-controller reconcile** — `internal/serverconfig/serverconfig_controller_test.go`
- **bc-controller reconcile** — `internal/backupconfig/backupconfig_controller_test.go`

Any persistence change requires a round-trip test (write → read back → assert).

## Test discipline

- **Every behavioral change gets a test.** New field, new API response, new
  interface — include the test in the same PR. Don't wait to be asked.
- **Run tests after writing them.** Always `make test` before handing back.
  If they fail, fix before reporting done.
- **Test at the lowest isolatable level.** Unit for pure logic (parsing,
  filtering, delta computation); envtest for K8s apply/watch behavior;
  e2e only when the full pipeline matters.
- **Interfaces at external boundaries.** OCI clients, HTTP clients, and
  other I/O-bound deps must be injected via interface so tests can swap
  in fakes. Never make external calls non-injectable.

## Releases

Four components, four tag namespaces:

| Component | Tag prefix | Image name |
|---|---|---|
| cb-controller | `controller/v*` | `armadaeksatest.azurecr.io/configbundle-controller` |
| cb-bundler | `bundler/v*` | `armadaeksatest.azurecr.io/configbundle-bundler` |
| sc-controller | `serverconfig/v*` | `armadaeksatest.azurecr.io/serverconfig-controller` |
| bc-controller | `backupconfig/v*` | `armadaeksatest.azurecr.io/backupconfig-controller` |

Versions derive automatically via `git describe --tags --match '<prefix>/v*'`.
Cut a tag, log into ACR, push:

```bash
git tag controller/v0.0.4
az acr login --name armadaeksatest
make push-controller
```

Or release everything at once:

```bash
git tag controller/v0.0.4
git tag bundler/v0.0.5
git tag serverconfig/v0.0.3
git tag backupconfig/v0.0.2
make push-all
```

Deploying to a cluster is a separate step — see
[`docs/playbooks/deploy-to-edge.md`](docs/playbooks/deploy-to-edge.md).

**Version bump conventions** (prototype status — everything is `v0.0.x`):
- **Patch** (`v0.0.X` → `v0.0.X+1`) — bug fixes, small non-breaking additions
- **Minor** (`v0.X.0` → `v0.X+1.0`) — new features, non-breaking
- **Major** (`vX.0.0` → `vX+1.0.0`) — breaking changes. Coordinate with orbital.

**bundler deploys separately.** `configbundle-bundler` runs as a sidecar in
Orbital's cloud AKS pod, not on the edge. After pushing a new bundler tag,
coordinate the orbital deploy with whoever manages that repo.

## Deploy to edge

The colo-dev-main edge cluster runs cb-controller + sc-controller +
bc-controller in the `configbundle-system` namespace. Deploy sequence
documented in [`docs/playbooks/deploy-to-edge.md`](docs/playbooks/deploy-to-edge.md).

**Do not deploy to live edge without explicit permission.** Default to
minikube for "validate" requests; ask before pushing to ACR or restarting
colo-dev-main deployments.

## Go conventions

- **Error wrapping** — `fmt.Errorf("...: %w", err)`. Never discard or log-and-return.
- **Context** — always the first argument: `func Foo(ctx context.Context, ...)`.
- **Constructors** — `New[Type]`, e.g. `NewConsumeServer`, `NewDivergenceReporter`.
- **`cmd/` is thin** — entry points only. All logic in `internal/`.
- **Tests** — table-driven with `t.Run`. Avoid helpers that obscure the failure site.
- **No `init()` functions** (except CRD scheme registration).
- **No global variables.**
- **No `panic()` outside `main()`.**

## Working style

- Don't add comments that restate what the code does.
- Don't refactor code that wasn't part of the request — ask first.
- Don't add third-party packages without asking first.
- Only touch files relevant to the task.
- No TODOs or placeholder comments.
- No error handling for scenarios that can't happen.
- **Domain-specific decisions live inline in the topic doc's `## Settled
  Decisions` section (`docs/reference/<DOMAIN>.md`), not CLAUDE.md.** CLAUDE.md
  is for cross-cutting decisions only. Do NOT create separate ADR files under
  `docs/decisions/` — the cross-referencing burden outweighs the audit-trail
  benefit for a small team. Historical decisions live in git history.
- **At PR phase: if the diff introduces a settled decision, add a dated
  bullet to the relevant topic doc's `## Settled Decisions` section in the
  same commit — do not defer.**

## AI-assistant usage

Everyone works with Claude Code on this repo. Conventions are shared so
context compounds instead of drifting.

### Model selection

**Default: Sonnet** for all implementation work.

**Switch to Opus (`/effort max`) for:**
- Designing a new subsystem with long-term architectural impact
- Security-sensitive design (auth, signing, permissions, key management)
- Planning a new spike for the first time
- Reviewing a completed spike against architectural invariants
- Any task touching 3+ domains simultaneously

**Switch back to Sonnet (`/effort normal`)** once design is settled.

### Conversation prefixes

Gate how Claude responds. Use them consistently:

- **`thoughts:` / `discuss:`** — exploratory dialogue. Claude responds
  conversationally only. No code, no file edits.
- **`propose:`** — written design proposal for review before any implementation.
- **`critique:`** — cold critical read of a specific document. Switch to Opus
  first. Structured findings with priority ratings.
- **`challenge:`** — stress-test a design thesis. Claude is adversarial.
- **`validate:`** — confirm reasoning against docs and knowledge base.
- **No prefix** — Claude implements.

### User-level Claude skills

Install once per machine:

- **`/effort`** — switch between Sonnet and Opus mid-session
- **`/wrap-up`** — end-of-session housekeeping: updates CLAUDE.md, saves
  memories, appends to ROADMAP.md's Recent Accomplishments (pruned to 5),
  suggests a commit message
- **`/compact`** — compresses prior conversation when the window gets large

### Repo-scoped commands

Available in this repo via `.claude/commands/`:

- **`/roadmap`** — reads ROADMAP.md and reports current state of work
- **`/propose`** — write a design proposal, no code
- **`/review`** — cold review of the current diff against project conventions
- **`/pr-context`** — compare the diff against topic docs (`docs/reference/*.md`),
  surface what needs updating in the same PR
- **`/distill`** — condense source docs into a topic reference doc
- **`/score`** — assess current session's AI CaC health

### Session hygiene

- Start each session by reading `ROADMAP.md` (or run `/roadmap`) to orient.
- Work one item per session. Unrelated tasks pollute context.
- At session end, run `/wrap-up` to update CLAUDE.md, save any decisions,
  and update ROADMAP.md.
- Commit before ending. Don't leave in-progress Claude work uncommitted.
- Claude stages changes; **the user commits.** Don't run `git commit`
  from Claude unless explicitly told to.

### CLAUDE.md sync rule

Any session that produces a new architectural decision, settled constraint,
or domain convention **must** include the corresponding CLAUDE.md or
domain-file update in the same PR as the code. The PR reviewer checks this.

This is how the team's AI context compounds in quality over time instead
of drifting.

### Context Commit Protocol

The sync rule is enforced through a five-step workflow at PR time:

1. **Context Load** — at task start, Claude picks the relevant
   `docs/reference/<DOMAIN>.md` from the README's Where-to-go-next table
   and signals what it loaded. *"Reading CRD.md before starting."* Do not
   proceed without this signal.
2. **Decision Surface** — during work, Claude flags decisions not covered
   in the topic doc. *"This pattern isn't in CRD.md — flagging as a
   candidate for context commit."*
3. **Context Diff** — before opening the PR, run `/pr-context [domain]`.
   Claude reads the diff against the topic doc and surfaces proposed
   additions. Review and approve or reject each one.
4. **Context Commit** — approved additions are written to the topic doc's
   `## Settled Decisions` section (dated bullet) and committed in the
   same PR as the code. Not in a follow-up.
5. **Merge** — reviewer verifies the doc + code are in lockstep.

## Documentation conventions

- **Only two top-level markdown files** — this file (CONTRIBUTING.md) and
  README.md. Additional root markdowns are banned except for generated
  ones (CLAUDE.md, ROADMAP.md). No ONBOARDING.md / getting-started.md /
  quickstart.md — team culture belongs in Notion, and setup steps belong
  in the README's Quick Start section.
- **Deeper content under `docs/`:**
  - `docs/reference/<DOMAIN>.md` — topic doc with current state,
    conventions, and a `## Settled Decisions` section (dated bullets
    inline; when a decision changes, UPDATE the bullet in place rather
    than creating a superseding record)
  - `docs/playbooks/<recipe>.md` — canonical recipes for common changes
  - `docs/runbooks/<task>.md` — operational walkthroughs
  - `docs/research/` — investigation notes (informs decisions, not decisions)
- **No `docs/decisions/` directory** — the cross-referencing burden ("I'm
  reading EDGE.md, don't know to check decisions/010") outweighs the
  audit-trail benefit for a small team. Historical decisions live in
  git-blame if compliance ever requires them.
- **Delete completed plans.** Living plans clutter the doc surface; git
  history preserves them if needed.

## PR process

1. Branch named after the ROADMAP item (`wrk-NNN-<slug>` or descriptive).
2. Every behavioral change gets a test in the same commit.
3. If the diff introduces a settled decision, update CLAUDE.md or the
   relevant `docs/reference/<DOMAIN>.md` in the same commit.
4. Regenerate on schema changes: `make generate manifests` before opening
   the PR. Include the regenerated files in the commit.
5. `make test` green.
6. Commit message: capability-first, ≤15 words, no trailing body. Match
   the pattern of recent commits (`git log --oneline -5`).
7. Open PR, filling in `.github/pull_request_template.md`. See the
   provenance-over-disclosure note below.
8. Request at least one human reviewer.
9. On merge, mark the ROADMAP item done. Save the roadmap update in the
   PR (or a follow-up commit on main) — don't leave it stale.

### PR provenance over AI disclosure

AI assistance is assumed on every PR in this repo, so *disclosure* that
AI was used carries no information and no longer belongs in commit
trailers, PR templates, or activity logs. What actually varies per
change — and what a reviewer needs to see — is **provenance**:

- **Intent** — what problem this PR solves and the intended behavior
  (`## Why`).
- **Steering** — the framing, constraints, and redirections that shaped
  the diff (`## How it came together`). A curated story of the
  decisions, not a transcript.
- **Verification** — what was checked and how you know it works
  (`## Verification`).
- **Human ownership** — an accountability checkbox: *"I have read every
  changed line and take responsibility for this PR."* That's the teeth.

The PR template enforces this shape. Do NOT reintroduce `AI-model:` /
`AI-settled:` commit trailers, an `AGENTS.md` disclosure log, an `AI.md`
convention file, or a `Co-authored-by: Claude` trailer — a constant is
not signal. This convention mirrors what the sibling `orbital` repo
settled on, so PRs across the two repos read the same way.
