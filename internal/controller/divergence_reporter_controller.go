package controller

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	armadav1 "github.com/armada/configbundle/api/v1"
)

// SetupWithManager registers DivergenceReporter as a controller that watches ConfigBundle CRs,
// a one-shot bootstrap Runnable that rehydrates lastManifests from per-CR ConfigMaps at
// startup (so restarts don't lose the intent baseline), and (when enabled) a periodic
// heartbeat that re-syncs the per-CR posted-hash cache.
func (r *DivergenceReporter) SetupWithManager(mgr ctrl.Manager) error {
	if r.enabled {
		if err := mgr.Add(&lastManifestLoader{reporter: r}); err != nil {
			return fmt.Errorf("register last-manifest loader: %w", err)
		}
	}
	if r.enabled && r.heartbeatInterval > 0 {
		if err := mgr.Add(&divergenceHeartbeat{reporter: r}); err != nil {
			return fmt.Errorf("register divergence heartbeat: %w", err)
		}
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&armadav1.ConfigBundle{}).
		WithEventFilter(r.predicate()).
		Named("divergence-reporter").
		Complete(r)
}

// lastManifestLoader is a one-shot manager.Runnable that rehydrates the
// reporter's in-memory lastManifests map from each ConfigBundle's per-CR
// ConfigMap at startup. Runs once after the manager's cache syncs and returns.
// Without this, every controller restart opens a cold-start window where the
// reporter has no intent baseline and either skips POSTs (after my earlier
// guard) or wipes orb's state (pre-guard) — until the next bundle dispatch.
type lastManifestLoader struct {
	reporter *DivergenceReporter
}

func (l *lastManifestLoader) Start(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("divergence-reporter").WithName("bootstrap")
	// ConfigBundle is cluster-scoped — list cluster-wide.
	var list armadav1.ConfigBundleList
	if err := l.reporter.Client.List(ctx, &list); err != nil {
		logger.Info("list ConfigBundles failed; lastManifests will rely on next dispatch", "err", err.Error())
		return nil
	}
	loaded := 0
	for _, cb := range list.Items {
		spec, err := readLastAppliedSpec(ctx, l.reporter.Client, l.reporter.namespace, cb.Name)
		if err != nil {
			logger.Info("read last-applied-spec failed", "configbundle", cb.Name, "err", err.Error())
			continue
		}
		if spec == nil {
			continue
		}
		l.reporter.SetLastManifest(cb.Name, *spec)
		loaded++
	}
	logger.Info("rehydrated lastManifests", "configbundles", len(list.Items), "loaded", loaded)
	return nil
}

// divergenceHeartbeat is a manager.Runnable that ticks every reporter.heartbeatInterval,
// lists ConfigBundles in the configured namespace, clears each CR's lastPostedHash entry,
// and triggers a reconcile. Bounds the staleness window for the "orb wipe" failure mode —
// orb's persistent divergence store can be lost (PVC failure, manual wipe, fresh edge
// deploy) and the reporter's in-memory hash cache would otherwise dedup the post
// forever because no managedFields event fires to invalidate it.
type divergenceHeartbeat struct {
	reporter *DivergenceReporter
}

func (h *divergenceHeartbeat) Start(ctx context.Context) error {
	t := time.NewTicker(h.reporter.heartbeatInterval)
	defer t.Stop()
	logger := log.FromContext(ctx).WithName("divergence-reporter").WithName("heartbeat")
	logger.Info("heartbeat started", "interval", h.reporter.heartbeatInterval)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			h.tick(ctx, logger)
		}
	}
}

func (h *divergenceHeartbeat) tick(ctx context.Context, logger logrLogger) {
	// ConfigBundle is cluster-scoped — list cluster-wide.
	var list armadav1.ConfigBundleList
	if err := h.reporter.Client.List(ctx, &list); err != nil {
		logger.Info("list ConfigBundles failed", "err", err.Error())
		return
	}
	if len(list.Items) == 0 {
		return
	}
	// Only force-repost CBs that actually have local:* claims. The heartbeat's
	// purpose is recovering from an orb state wipe — but if a CB has no local
	// overrides, orb's view is "empty" with or without a wipe, so re-posting
	// an empty payload would be noise. CBs with claims get the dedup hash
	// cleared so Reconcile re-POSTs even if the override set is unchanged.
	var withClaims []armadav1.ConfigBundle
	for _, cb := range list.Items {
		if len(extractAdminPaths(cb.ManagedFields)) > 0 {
			withClaims = append(withClaims, cb)
		}
	}
	h.reporter.mu.Lock()
	for _, cb := range withClaims {
		delete(h.reporter.lastPostedHash, types.NamespacedName{Name: cb.Name})
	}
	h.reporter.mu.Unlock()
	// Trigger reconcile for each CR with claims. Direct call bypasses the work
	// queue — acceptable here because we ARE the periodic re-sync; there's no
	// event debouncing to honor.
	for _, cb := range withClaims {
		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: cb.Name}}
		if _, err := h.reporter.Reconcile(ctx, req); err != nil {
			logger.Error(err, "reconcile failed", "configbundle", cb.Name)
		}
	}
	logger.Info("heartbeat tick complete", "configbundles", len(list.Items), "withClaims", len(withClaims))
}

// Reconcile is called when a ConfigBundle CR's local:* managed fields change.
// It debounces, reads the mapping ConfigMap, computes the override set, deduplicates
// by content hash, and POSTs to orb's divergence intake.
func (r *DivergenceReporter) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	logger := log.FromContext(ctx).WithName("divergence-reporter")

	if !r.enabled {
		return reconcile.Result{}, nil
	}

	r.mu.Lock()
	last := r.lastEventAt[req.NamespacedName]
	r.mu.Unlock()

	// Zero lastEventAt means startup reconcile — elapsed is huge, proceed immediately.
	elapsed := time.Since(last)
	if !last.IsZero() && elapsed < r.debounceWindow {
		return reconcile.Result{RequeueAfter: r.debounceWindow - elapsed}, nil
	}

	var cb armadav1.ConfigBundle
	if err := r.Client.Get(ctx, req.NamespacedName, &cb); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	warnNonConformingManagers(logger, cb.Name, cb.ManagedFields)

	r.lastManifestsMu.RLock()
	lastManifest, haveLastManifest := r.lastManifests[cb.Name]
	r.lastManifestsMu.RUnlock()

	// Cold-start guard: without a lastManifest we don't know what the intent
	// values are, so every local:admin claim looks "intent-absent" and
	// extractOverrides returns nil. Posting nil to orb is REPLACE-not-merge —
	// it would wipe orb's last-known good divergence set. Skip until the next
	// orb-import dispatch (consume.go) populates lastManifests for this CR.
	if !haveLastManifest {
		logger.Info("no lastManifest yet (controller cold start, awaiting next bundle import); skipping post to avoid wiping orb's state", "configbundle", req.Name)
		return reconcile.Result{}, nil
	}

	overrides := r.extractOverrides(&cb, lastManifest)
	payload := DivergencePayload{Overrides: overrides}

	h := contentHash(payload)
	r.mu.Lock()
	sameHash := r.lastPostedHash[req.NamespacedName] == h
	lastHadOverrides := r.lastPostedHadOverrides[req.NamespacedName]
	r.mu.Unlock()
	if sameHash {
		logger.V(1).Info("override set unchanged, skipping POST", "configbundle", req.Name)
		return reconcile.Result{}, nil
	}

	// Steady-state quiet: when this Reconcile would just send "still no overrides"
	// (current empty AND last post was also empty), skip the POST and the log.
	// Hash mismatch in this case comes from heartbeat clearing or fresh reporter
	// startup, not from an actual state change. Transitions in either direction
	// (empty→non-empty, non-empty→empty) still POST and log — those are meaningful.
	if len(overrides) == 0 && !lastHadOverrides {
		r.mu.Lock()
		r.lastPostedHash[req.NamespacedName] = h
		r.mu.Unlock()
		return reconcile.Result{}, nil
	}

	if err := r.postToOrb(ctx, payload); err != nil {
		logger.Error(err, "POST divergence failed", "configbundle", req.Name, "url", r.intakeURL)
		return reconcile.Result{}, fmt.Errorf("POST divergence: %w", err)
	}

	r.mu.Lock()
	r.lastPostedHash[req.NamespacedName] = h
	r.lastPostedHadOverrides[req.NamespacedName] = len(overrides) > 0
	r.mu.Unlock()

	if len(overrides) > 0 {
		logger.Info("reported divergence", "configbundle", req.Name, "overrides", len(overrides))
	} else {
		// Non-empty → empty transition. The override set was just cleared (e.g.
		// local:admin released the field, or the cloud admin's takeover landed).
		// Worth surfacing as a distinct log line from the steady-state quiet path.
		logger.Info("cleared divergence (override set went empty)", "configbundle", req.Name)
	}
	return reconcile.Result{}, nil
}

func (r *DivergenceReporter) predicate() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			if !localManagersChanged(e.ObjectOld.GetManagedFields(), e.ObjectNew.GetManagedFields()) {
				return false
			}
			// ConfigBundle is cluster-scoped — Namespace is always "".
			key := types.NamespacedName{Name: e.ObjectNew.GetName()}
			r.mu.Lock()
			r.lastEventAt[key] = time.Now()
			r.mu.Unlock()
			return true
		},
		// Fire on Create so a controller restart / rollout immediately re-establishes
		// the divergence picture on orb. The reconcile path is naturally cold-start-safe:
		//   - lastEventAt is zero for these → debounce check passes (treated as startup)
		//   - lastPostedHash is empty → POST fires unconditionally (no dedup race)
		//   - lastManifest guard skips the POST if bootstrap hasn't loaded it yet
		// This closes the up-to-heartbeat-interval observability gap that would
		// otherwise exist after every rollout.
		CreateFunc:  func(_ event.CreateEvent) bool { return true },
		DeleteFunc:  func(_ event.DeleteEvent) bool { return false },
		GenericFunc: func(_ event.GenericEvent) bool { return false },
	}
}

// localManagersChanged reports whether the set of local:* manager fields changed between two managed-field slices.
func localManagersChanged(old, new []metav1.ManagedFieldsEntry) bool {
	extract := func(fields []metav1.ManagedFieldsEntry) map[string][]byte {
		m := make(map[string][]byte)
		for _, e := range fields {
			if strings.HasPrefix(e.Manager, "local:") && e.FieldsV1 != nil {
				m[e.Manager] = e.FieldsV1.Raw
			}
		}
		return m
	}
	oldMap := extract(old)
	newMap := extract(new)
	if len(oldMap) != len(newMap) {
		return true
	}
	for k, v := range oldMap {
		nv, ok := newMap[k]
		if !ok || !bytes.Equal(v, nv) {
			return true
		}
	}
	return false
}
