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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

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

func (r *ConfigBundleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var cb armadav1.ConfigBundle
	if err := r.Get(ctx, req.NamespacedName, &cb); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("reconciling ConfigBundle", "name", cb.Name, "servers", len(cb.Spec.Servers))

	for _, server := range cb.Spec.Servers {
		if err := r.applyServerConfig(ctx, &cb, server); err != nil {
			log.Error(err, "failed to apply ServerConfig", "serviceTag", server.ServiceTag)
			return ctrl.Result{}, err
		}
		log.Info("applied ServerConfig", "name", strings.ToLower(server.ServiceTag))
	}

	return ctrl.Result{}, nil
}

// applyServerConfig creates or updates a ServerConfig CR for the given server
// using Server-Side Apply with field manager "configbundle-controller".
func (r *ConfigBundleReconciler) applyServerConfig(ctx context.Context, cb *armadav1.ConfigBundle, server armadav1.ServerSpec) error {
	sc := &armadav1.ServerConfig{
		TypeMeta: metav1.TypeMeta{
			APIVersion: armadav1.GroupVersion.String(),
			Kind:       "ServerConfig",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      strings.ToLower(server.Hostname),
			Namespace: cb.Namespace,
		},
		Spec: armadav1.ServerConfigSpec{
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
// Owns(ServerConfig) ensures changes to child CRs re-trigger reconciliation of the parent.
func (r *ConfigBundleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&armadav1.ConfigBundle{}).
		Owns(&armadav1.ServerConfig{}).
		Named("configbundle").
		Complete(r)
}
