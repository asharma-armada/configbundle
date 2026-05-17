# Context Routing Index

Before starting work in a specific area, read the relevant domain file. Only load what you need for the current task.

| Working on | Read this first |
|---|---|
| Bundler HTTP service, enricher API, Orbital GraphQL integration | [`api-context.md`](api-context.md) |
| OCI artifact structure, layers, media types, signing, tags | [`bundle-context.md`](bundle-context.md) |
| CRD types, ConfigBundle CR, kubebuilder annotations, SSA | [`crd-context.md`](crd-context.md) |
| Edge agent, Zot registry, cosign verification, divergence | [`edge-context.md`](edge-context.md) |
| Orbital GraphQL data model, bundler query logic, ConfigBundle manifest YAML, local overrides | [`orbital-context.md`](orbital-context.md) |

---

## Adding a new domain

1. Copy `_TEMPLATE.md` → `[domain]-context.md`
2. Add a row to this table
3. Add the domain to the routing table in `CLAUDE.md`

## Updating domain files

Domain files are updated at PR phase — not mid-task. When a PR introduces a settled decision, update the relevant file in the same commit.

> "We changed how we handle auth tokens. Update context."  
> → Claude reads the diff + `auth-context.md`, appends the decision.
