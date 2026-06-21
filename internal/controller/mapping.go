package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/yaml"

	armadav1 "github.com/armada/configbundle/api/v1"
)

// LastAppliedSpecKey is the ConfigMap data key under which the most recent
// controller-applied manifest spec is stored. The reporter rehydrates its
// in-memory lastManifests from this on startup, eliminating the cold-start
// window where divergences can't be computed (because the in-memory map is
// empty until the next bundle dispatch).
const LastAppliedSpecKey = "last-applied-spec.yaml"

// MappingConfigMapName returns the name of the per-CR state ConfigMap for a
// ConfigBundle. The "-mapping" suffix is retained for backward compatibility
// with the previous design (the CM still has the same name); after ADR-011 the
// CM carries only the last-applied-spec snapshot, not the deleted mapping.json
// payload.
func MappingConfigMapName(cbName string) string {
	return cbName + "-mapping"
}

// writeLastAppliedSpec persists the controller-applied manifest spec to the
// per-CR ConfigMap under LastAppliedSpecKey. The OwnerReference ties lifecycle
// to the CR — K8s GC deletes the CM when the parent CB is deleted.
//
// The reporter rehydrates lastManifests from this on controller startup so a
// fresh process doesn't lose its intent baseline. Without persistence, every
// controller restart opens a recovery-required window until the next bundle
// dispatches — divergences would either not report or accidentally wipe orb's
// state (when extractOverrides returns nil from a missing baseline).
func writeLastAppliedSpec(ctx context.Context, c client.Client, namespace, cbName string, spec armadav1.ConfigBundleSpec) error {
	yamlBytes, err := yaml.Marshal(spec)
	if err != nil {
		return fmt.Errorf("marshal spec: %w", err)
	}
	// Fetch the CR for the OwnerReference UID. The apply that produced this
	// spec has already succeeded at this point, so the CR exists.
	// ConfigBundle is cluster-scoped — Get with name only.
	var cb armadav1.ConfigBundle
	if err := c.Get(ctx, types.NamespacedName{Name: cbName}, &cb); err != nil {
		return fmt.Errorf("get ConfigBundle for ownerRef: %w", err)
	}
	ownerRef := metav1.OwnerReference{
		APIVersion:         armadav1.GroupVersion.String(),
		Kind:               "ConfigBundle",
		Name:               cb.Name,
		UID:                cb.UID,
		Controller:         ptr.To(true),
		BlockOwnerDeletion: ptr.To(true),
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      MappingConfigMapName(cbName),
				Namespace: namespace,
			},
		}
		_, err := controllerutil.CreateOrUpdate(ctx, c, cm, func() error {
			if cm.Labels == nil {
				cm.Labels = map[string]string{}
			}
			cm.Labels["armada.ai/configbundle"] = cbName
			cm.Labels["armada.ai/component"] = "mapping"
			cm.OwnerReferences = []metav1.OwnerReference{ownerRef}
			if cm.Data == nil {
				cm.Data = map[string]string{}
			}
			cm.Data[LastAppliedSpecKey] = string(yamlBytes)
			return nil
		})
		return err
	})
}

// readLastAppliedSpec loads the spec persisted by writeLastAppliedSpec. Returns
// (nil, nil) when the ConfigMap doesn't exist or the key is absent — caller
// treats this as "no baseline known," same as the in-memory cold-start case.
func readLastAppliedSpec(ctx context.Context, c client.Client, namespace, cbName string) (*armadav1.ConfigBundleSpec, error) {
	var cm corev1.ConfigMap
	if err := c.Get(ctx, types.NamespacedName{Name: MappingConfigMapName(cbName), Namespace: namespace}, &cm); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get ConfigMap: %w", err)
	}
	raw, ok := cm.Data[LastAppliedSpecKey]
	if !ok || raw == "" {
		return nil, nil
	}
	var spec armadav1.ConfigBundleSpec
	if err := yaml.Unmarshal([]byte(raw), &spec); err != nil {
		return nil, fmt.Errorf("unmarshal spec: %w", err)
	}
	return &spec, nil
}
