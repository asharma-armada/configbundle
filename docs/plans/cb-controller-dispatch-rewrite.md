# CB Controller Dispatch Rewrite

## What this covers

1. Replace `/consume` + `/mapping` endpoints on cb-controller with a single `POST /dispatch` that routes by `Content-Type`.
2. Replace the in-memory `MappingCache` with a per-ConfigBundle `ConfigMap` for mapping storage.
3. Replace the polling `DivergenceReporter` ticker with an event-driven controller-runtime watch + debouncer.
4. Add content-hash dedup so identical override sets don't re-POST.

## Motivation

- `/consume` and `/mapping` are two separate endpoints doing the same job (receiving orb layer dispatch). A single `/dispatch` endpoint with Content-Type routing is the idiomatic pattern and simplifies orb's consumer registration.
- In-memory `MappingCache` is lost on restart; ConfigMap storage persists across pod restarts and is GC'd when the parent CR is deleted.
- Ticker-based polling fires regardless of changes; event-driven reconciliation only fires when `local:*` managed fields actually change, reducing noise and CPU.
- Content-hash dedup prevents re-POSTing unchanged override sets to orb's intake.

## Changes

### Step 1: `POST /dispatch` (consume.go)

- Replace `mux.HandleFunc("POST /consume", s.handleConsume)` and `mux.HandleFunc("POST /mapping", s.handleMapping)` with `mux.HandleFunc("POST /dispatch", s.handleDispatch)`.
- `handleDispatch` reads `Content-Type` and routes to `handleManifestBody` or `handleMappingBody`, else 415.
- `handleMappingBody` writes the mapping to a ConfigMap (via `writeMappingConfigMap`) instead of in-memory cache.
- Remove `s.mappings *MappingCache` and `WithMappingCache` option.

### Step 2: Mapping → ConfigMap (mapping.go)

- Add `MappingConfigMapName`, `writeMappingConfigMap`, `readMappingConfigMap`.
- Remove `MappingCache`, `NewMappingCache`.

### Step 3–5: Event-driven reporter (divergence_reporter.go + divergence_reporter_controller.go)

- Rewrite `DivergenceReporter` struct to add `debounceWindow`, `lastEventAt`, `lastPostedHash` maps.
- Remove `Start()`, `NeedsLeaderElection()`, polling `report()`.
- Add `contentHash()` for dedup.
- Update `extractOverrides` to take `lastManifest` as parameter.
- New file `divergence_reporter_controller.go`: `SetupWithManager`, `Reconcile`, `predicate`, `localManagersChanged`.

### Step 6: Wiring (cmd/main.go)

- Replace `DivergenceReporterInterval` with `DivergenceReporterDebounce`.
- Replace `WithDivergenceInterval` with `WithDivergenceDebounce`.
- Remove `mappingCache`, `WithMappingCache`, `mgr.Add(reporter)`.
- Add `reporter.SetupWithManager(mgr)`.

### Step 7: Tests

- Update `consume_test.go`: test `handleDispatch` for routing; rename internal method calls.
- Update `divergence_reporter_test.go`: add `localManagersChanged` and `contentHash` unit tests; update `extractOverrides` calls.
- Update `configbundle_controller_test.go`: add ConfigMap round-trip and GC envtest tests; update POSTs test to use `Reconcile` and ConfigMap.
- Update `suite_test.go`: register reporter controller with `SetupWithManager`.
- Update `mapping_test.go`: remove `TestMappingCache_StoreLoad`.
