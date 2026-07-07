## What

<!-- What does this PR do? One or two sentences. -->

## Why

<!-- Why is this change needed? Link to the ROADMAP item or issue. State
the intended behavior explicitly — agent-assisted diffs aren't hand-typed
line by line, so the intent isn't implicit in the effort. -->

## How it came together

<!-- Provenance: how this change was arrived at — not who typed it (AI
assistance is assumed across this project, so there's nothing to
disclose). The load-bearing framing and the decisions you made steering
it — e.g. "asked for X under constraint Y; redirected when it drifted
to Z" — plus alternatives considered and rejected. The curated story,
not a transcript. Skip for trivial changes. -->

## Verification

<!-- What you checked and how you know it works: tests added/run, manual
QA (minikube apply, e2e on colo-dev-main if applicable), edge cases.
Call out anything you want the reviewer to scrutinize or that you're
unsure about. -->

## Checklist

- [ ] I have read every changed line and take responsibility for this PR
- [ ] `make test` green; behavioral changes covered by a test in the same commit
- [ ] Settled decisions or conventions updated inline in `docs/reference/<DOMAIN>.md` (or `CLAUDE.md` if cross-cutting)
- [ ] CRD or orbital-schema changes: `make generate manifests` run, regenerated files and updated `config/samples/*.yaml` included
- [ ] Bundler ↔ controller contract preserved (enricher API shape, `POST /dispatch` media types, orbital GraphQL edge names)
- [ ] ROADMAP.md status updated if this closes or advances a WRK item
