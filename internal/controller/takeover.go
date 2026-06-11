package controller

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	armadav1 "github.com/armada/configbundle/api/v1"
)

// processTakeover runs the second pass of the consume pipeline: for each takeover
// entry, it builds a minimal ConfigBundle spec containing only the targeted field
// and applies it with ForceOwnership, reclaiming ownership from local:admin.
//
// This runs after the normal SSA apply and runs regardless of whether the normal
// apply succeeded (per ADR-006).
func (s *ConsumeServer) processTakeover(ctx context.Context, spec armadav1.ConfigBundleSpec) error {
	if len(spec.Takeover) == 0 {
		return nil
	}

	logger := log.FromContext(ctx).WithName("takeover")
	var errs []error

	for _, entry := range spec.Takeover {
		if err := s.applyTakeoverEntry(ctx, spec, entry); err != nil {
			logger.Error(err, "takeover entry failed",
				"serviceTag", entry.ServiceTag, "field", entry.Field, "orbId", entry.OrbID)
			errs = append(errs, err)
			continue
		}
		logger.Info("takeover succeeded",
			"serviceTag", entry.ServiceTag, "field", entry.Field, "orbId", entry.OrbID)
	}

	if len(errs) > 0 {
		return fmt.Errorf("%d of %d takeover entries failed", len(errs), len(spec.Takeover))
	}
	return nil
}

// applyTakeoverEntry builds a minimal ConfigBundle spec containing only the server
// entry and field targeted by the takeover, then applies with ForceOwnership.
func (s *ConsumeServer) applyTakeoverEntry(ctx context.Context, fullSpec armadav1.ConfigBundleSpec, entry armadav1.TakeoverEntry) error {
	// Find the server entry in the full spec.
	var targetServer *armadav1.ServerSpec
	for i := range fullSpec.Servers {
		if fullSpec.Servers[i].ServiceTag == entry.ServiceTag {
			targetServer = &fullSpec.Servers[i]
			break
		}
	}
	if targetServer == nil {
		return fmt.Errorf("server with serviceTag %q not found in spec", entry.ServiceTag)
	}

	// Build a minimal server spec that includes only the identity fields
	// (serviceTag is the listMapKey, required for SSA to identify the entry)
	// plus the targeted field.
	minimalServer := armadav1.ServerSpec{
		ServiceTag: targetServer.ServiceTag,
	}

	if err := setFieldOnServer(&minimalServer, targetServer, entry.Field); err != nil {
		return fmt.Errorf("set takeover field: %w", err)
	}

	apply := &armadav1.ConfigBundle{
		TypeMeta: metav1.TypeMeta{
			APIVersion: armadav1.GroupVersion.String(),
			Kind:       "ConfigBundle",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      fullSpec.Datacenter,
			Namespace: s.namespace,
		},
		Spec: armadav1.ConfigBundleSpec{
			Datacenter: fullSpec.Datacenter,
			Servers:    []armadav1.ServerSpec{minimalServer},
		},
	}

	return s.Client.Patch(ctx, apply, client.Apply,
		client.FieldOwner("configbundle-controller"),
		client.ForceOwnership,
	)
}

// setFieldOnServer copies a single field (by JSON tag name) from src to dst.
// It checks ServerSpec top-level fields first, then IdracSpec fields.
// Uses reflection so adding new fields to the CRD types is sufficient —
// no switch cases to maintain.
func setFieldOnServer(dst, src *armadav1.ServerSpec, field string) error {
	// Try ServerSpec top-level fields
	if copyStructFieldByJSONTag(reflect.ValueOf(dst).Elem(), reflect.ValueOf(src).Elem(), field) {
		return nil
	}
	// Try IdracSpec fields
	if copyStructFieldByJSONTag(reflect.ValueOf(&dst.Idrac).Elem(), reflect.ValueOf(&src.Idrac).Elem(), field) {
		return nil
	}
	return fmt.Errorf("unknown takeover field %q", field)
}

// copyStructFieldByJSONTag finds a field on dst whose json tag matches jsonName,
// copies the value from the corresponding field on src, and returns true.
// Returns false if no matching field is found.
func copyStructFieldByJSONTag(dst, src reflect.Value, jsonName string) bool {
	t := dst.Type()
	for i := 0; i < t.NumField(); i++ {
		tag := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
		if tag == jsonName {
			dst.Field(i).Set(src.Field(i))
			return true
		}
	}
	return false
}
