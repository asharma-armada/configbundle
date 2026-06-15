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

// SetupWithManager registers DivergenceReporter as a controller that watches ConfigBundle CRs.
func (r *DivergenceReporter) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&armadav1.ConfigBundle{}).
		WithEventFilter(r.predicate()).
		Named("divergence-reporter").
		Complete(r)
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

	mapping, err := readMappingConfigMap(ctx, r.Client, req.Namespace, req.Name)
	if err != nil {
		if client.IgnoreNotFound(err) == nil {
			logger.Info("no mapping ConfigMap yet, skipping", "configbundle", req.Name)
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("read mapping ConfigMap: %w", err)
	}

	warnNonConformingManagers(logger, cb.Name, cb.ManagedFields)

	r.lastManifestsMu.RLock()
	lastManifest := r.lastManifests[cb.Name]
	r.lastManifestsMu.RUnlock()

	overrides := r.extractOverrides(&cb, mapping, lastManifest)
	payload := DivergencePayload{Overrides: overrides}

	h := contentHash(payload)
	r.mu.Lock()
	sameHash := r.lastPostedHash[req.NamespacedName] == h
	r.mu.Unlock()
	if sameHash {
		logger.Info("override set unchanged, skipping POST", "configbundle", req.Name)
		return reconcile.Result{}, nil
	}

	if err := r.postToOrb(ctx, payload); err != nil {
		return reconcile.Result{}, fmt.Errorf("POST divergence: %w", err)
	}

	r.mu.Lock()
	r.lastPostedHash[req.NamespacedName] = h
	r.mu.Unlock()

	logger.Info("reported divergence", "configbundle", req.Name, "overrides", len(overrides))
	return reconcile.Result{}, nil
}

func (r *DivergenceReporter) predicate() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			if !localManagersChanged(e.ObjectOld.GetManagedFields(), e.ObjectNew.GetManagedFields()) {
				return false
			}
			key := types.NamespacedName{Name: e.ObjectNew.GetName(), Namespace: e.ObjectNew.GetNamespace()}
			r.mu.Lock()
			r.lastEventAt[key] = time.Now()
			r.mu.Unlock()
			return true
		},
		CreateFunc:  func(_ event.CreateEvent) bool { return false },
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
