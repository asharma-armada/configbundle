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

// processTakeover runs the second pass of the consume pipeline. For each takeover
// entry, it injects the controller-intended value for the targeted field into
// patchSpec (which is the admin-omitted spec already prepared by applyManifest),
// then applies the resulting spec with ForceOwnership. Force only effectively
// wrests the takeover-target fields — every other field in patchSpec is already
// owned by configbundle-controller (or absent because admin owns it).
//
// This runs after the normal SSA apply and regardless of whether that apply
// succeeded (per ADR-006). It uses the SAME field manager as the normal apply
// so controller ownership is preserved consistently across both passes.
func (s *ConsumeServer) processTakeover(ctx context.Context, fullSpec armadav1.ConfigBundleSpec, patchSpec *armadav1.ConfigBundleSpec) error {
	if len(fullSpec.Takeover) == 0 {
		return nil
	}

	logger := log.FromContext(ctx).WithName("takeover")
	var errs []error

	// Inject each takeover target into patchSpec. Failures here are per-entry —
	// continue processing the rest. Apply happens once at the end.
	for _, entry := range fullSpec.Takeover {
		if err := injectTakeoverField(patchSpec, fullSpec, entry); err != nil {
			logger.Error(err, "takeover entry failed",
				"serverOrbId", entry.ServerOrbID, "field", entry.Field, "orbId", entry.OrbID)
			errs = append(errs, err)
			continue
		}
		logger.Info("takeover queued",
			"serverOrbId", entry.ServerOrbID, "field", entry.Field, "orbId", entry.OrbID)
	}

	if len(errs) == len(fullSpec.Takeover) {
		// All entries failed before reaching the apply — nothing left to do.
		return fmt.Errorf("%d of %d takeover entries failed", len(errs), len(fullSpec.Takeover))
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
		Spec: *patchSpec,
	}
	if err := s.Client.Patch(ctx, apply, client.Apply,
		client.FieldOwner("configbundle-controller"),
		client.ForceOwnership,
	); err != nil {
		return fmt.Errorf("takeover apply: %w", err)
	}

	if len(errs) > 0 {
		return fmt.Errorf("%d of %d takeover entries failed", len(errs), len(fullSpec.Takeover))
	}
	return nil
}

// injectTakeoverField sets the takeover-target field on patchSpec, copying the
// value from fullSpec (the orbital intent). If the target server is missing
// from patchSpec, it's added from fullSpec.
func injectTakeoverField(patchSpec *armadav1.ConfigBundleSpec, fullSpec armadav1.ConfigBundleSpec, entry armadav1.TakeoverEntry) error {
	var srcServer *armadav1.ServerSpec
	for i := range fullSpec.Servers {
		if fullSpec.Servers[i].OrbID == entry.ServerOrbID {
			srcServer = &fullSpec.Servers[i]
			break
		}
	}
	if srcServer == nil {
		return fmt.Errorf("server with orbId %q not found in spec", entry.ServerOrbID)
	}

	var dstServer *armadav1.ServerSpec
	for i := range patchSpec.Servers {
		if patchSpec.Servers[i].OrbID == entry.ServerOrbID {
			dstServer = &patchSpec.Servers[i]
			break
		}
	}
	if dstServer == nil {
		// Server entry was fully admin-owned and got dropped from patchSpec.
		// Append a minimal stub carrying just identity — only the takeover field
		// will be populated below. ServiceTag is included for CRD Required-field
		// validation; SSA listMapKey matches on orbId.
		patchSpec.Servers = append(patchSpec.Servers, armadav1.ServerSpec{
			OrbID:      srcServer.OrbID,
			ServiceTag: srcServer.ServiceTag,
		})
		dstServer = &patchSpec.Servers[len(patchSpec.Servers)-1]
	}

	if err := setFieldOnServer(dstServer, srcServer, entry.Field); err != nil {
		return fmt.Errorf("set takeover field: %w", err)
	}
	return nil
}

// setFieldOnServer copies a single field (by JSON tag name) from src to dst.
// It checks ServerSpec top-level fields first, then IdracSpec fields.
// Uses reflection so adding new fields to the CRD types is sufficient —
// no switch cases to maintain.
func setFieldOnServer(dst, src *armadav1.ServerSpec, field string) error {
	if copyStructFieldByJSONTag(reflect.ValueOf(dst).Elem(), reflect.ValueOf(src).Elem(), field) {
		return nil
	}
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
