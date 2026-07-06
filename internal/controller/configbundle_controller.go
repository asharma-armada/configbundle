/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	armadav1 "github.com/armada/configbundle/api/v1"
)

// ConfigBundleReconciler reconciles a ConfigBundle object.
type ConfigBundleReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=armada.ai,resources=configbundles,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=armada.ai,resources=configbundles/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=armada.ai,resources=configbundles/finalizers,verbs=update
// +kubebuilder:rbac:groups=armada.ai,resources=serverconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=armada.ai,resources=backupconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete

func (r *ConfigBundleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var cb armadav1.ConfigBundle
	if err := r.Get(ctx, req.NamespacedName, &cb); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("ConfigBundle deleted; children cleaned up by GC", "name", req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	isSpecChange := cb.Generation != cb.Status.ObservedGeneration
	if isSpecChange {
		log.Info("reconciling ConfigBundle",
			"name", cb.Name, "generation", cb.Generation,
			"servers", len(cb.Spec.Servers), "clusters", len(cb.Spec.Clusters))
	} else {
		log.V(1).Info("reconciling ConfigBundle (drift/owns event)",
			"name", cb.Name, "generation", cb.Generation)
	}

	for _, server := range cb.Spec.Servers {
		if err := r.applyServerConfig(ctx, &cb, server); err != nil {
			log.Error(err, "failed to apply ServerConfig", "serviceTag", server.ServiceTag)
			return ctrl.Result{}, err
		}
	}

	for _, cluster := range cb.Spec.Clusters {
		if err := r.applyBackupConfig(ctx, &cb, cluster); err != nil {
			log.Error(err, "failed to apply BackupConfig", "clusterOrbID", cluster.OrbID)
			return ctrl.Result{}, err
		}
	}

	if isSpecChange {
		log.Info("applied child CRs",
			"servers", len(cb.Spec.Servers),
			"clusters", len(cb.Spec.Clusters),
			"generation", cb.Generation)
		// Retry on conflict: ConsumeServer also writes Status (LastAppliedDigest, etc.)
		// which races our ObservedGeneration update. RetryOnConflict refetches and reapplies.
		err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			var fresh armadav1.ConfigBundle
			// ConfigBundle is cluster-scoped — no namespace.
			if err := r.Get(ctx, types.NamespacedName{Name: cb.Name}, &fresh); err != nil {
				return client.IgnoreNotFound(err)
			}
			if fresh.Status.ObservedGeneration == cb.Generation {
				return nil // someone else already wrote it
			}
			fresh.Status.ObservedGeneration = cb.Generation
			return r.Status().Update(ctx, &fresh)
		})
		if err != nil {
			log.Error(err, "failed to update ObservedGeneration")
			return ctrl.Result{}, err
		}
	} else {
		log.V(1).Info("applied child CRs (idempotent)",
			"servers", len(cb.Spec.Servers), "clusters", len(cb.Spec.Clusters))
	}
	return ctrl.Result{}, nil
}

// applyBackupConfig creates or updates a BackupConfig CR for the given cluster
// using Server-Side Apply with field manager "configbundle-controller". CR name
// is derived from the cluster's orbId — colons replaced with dashes so the name
// conforms to RFC 1123. Identity stays in spec.orbId; only the K8s resource
// name is transformed for syntactic compliance. Mirror of applyServerConfig.
func (r *ConfigBundleReconciler) applyBackupConfig(ctx context.Context, cb *armadav1.ConfigBundle, cluster armadav1.ClusterSpec) error {
	bc := &armadav1.BackupConfig{
		TypeMeta: metav1.TypeMeta{
			APIVersion: armadav1.GroupVersion.String(),
			Kind:       "BackupConfig",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: orbIDToK8sName(cluster.OrbID),
		},
		Spec: armadav1.BackupConfigSpec{
			OrbID:  cluster.OrbID,
			Velero: cluster.Velero,
			Etcd:   cluster.Etcd,
		},
	}

	if err := ctrl.SetControllerReference(cb, bc, r.Scheme); err != nil {
		return err
	}

	return r.Patch(ctx, bc, client.Apply,
		client.FieldOwner("configbundle-controller"),
		client.ForceOwnership,
	)
}

// orbIDToK8sName converts an Orbital orbId (e.g. "colo:cluster-001") into a
// valid RFC 1123 K8s resource name — replaces colon with dash, lowercases.
// Identity remains in spec.orbId; the name is just a label for kubectl listing.
func orbIDToK8sName(orbID string) string {
	return strings.ToLower(strings.ReplaceAll(orbID, ":", "-"))
}

// applyServerConfig creates or updates a ServerConfig CR for the given server
// using Server-Side Apply with field manager "configbundle-controller".
func (r *ConfigBundleReconciler) applyServerConfig(ctx context.Context, cb *armadav1.ConfigBundle, server armadav1.ServerSpec) error {
	hostname := ""
	if server.Hostname != nil {
		hostname = *server.Hostname
	}
	// ServerConfig is cluster-scoped — no namespace in metadata.
	sc := &armadav1.ServerConfig{
		TypeMeta: metav1.TypeMeta{
			APIVersion: armadav1.GroupVersion.String(),
			Kind:       "ServerConfig",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: strings.ToLower(hostname),
		},
		Spec: armadav1.ServerConfigSpec{
			OrbID:      server.OrbID,
			ServiceTag: server.ServiceTag,
			Hostname:   server.Hostname,
			OobIP:      server.OobIP,
			Idrac:      server.Idrac,
		},
	}

	if err := ctrl.SetControllerReference(cb, sc, r.Scheme); err != nil {
		return err
	}

	return r.Patch(ctx, sc, client.Apply,
		client.FieldOwner("configbundle-controller"),
		client.ForceOwnership,
	)
}

// SetupWithManager registers the controller with the manager.
//
// Predicates:
//   - For(ConfigBundle) uses GenerationChangedPredicate so reconcile fires only
//     on spec changes, not on Status updates we (or ConsumeServer) write.
//   - Owns(ServerConfig) ignores Delete events. Child deletions are cascaded by
//     Kubernetes GC when the parent is deleted; reacting to them would just
//     re-fire the parent reconcile N times (one per child). Update events still
//     fire so out-of-band drift on the child is restored.
func (r *ConfigBundleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&armadav1.ConfigBundle{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Owns(&armadav1.ServerConfig{}, builder.WithPredicates(predicate.Funcs{
			DeleteFunc: func(e event.DeleteEvent) bool { return false },
		})).
		Owns(&armadav1.BackupConfig{}, builder.WithPredicates(predicate.Funcs{
			DeleteFunc: func(e event.DeleteEvent) bool { return false },
		})).
		Named("configbundle").
		Complete(r)
}
