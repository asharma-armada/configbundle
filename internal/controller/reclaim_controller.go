package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/yaml"

	armadav1 "github.com/armada/configbundle/api/v1"
)

// ReclaimController watches ConfigBundle CRs for SSA-release events from
// local:* managers. When a field that previously had a local:* claim has no
// local:* claim anymore, the controller replays the last-imported manifest
// through ConsumeServer.applyManifest. The replay's SSA+ForceOwnership pass
// claims the released field with intent value.
//
// See ADR-009 (docs/decisions/009-edge-handback-via-release.md).
type ReclaimController struct {
	Client    client.Client
	consume   *ConsumeServer
	namespace string
}

func NewReclaimController(c client.Client, cs *ConsumeServer, ns string) *ReclaimController {
	return &ReclaimController{Client: c, consume: cs, namespace: ns}
}

func (r *ReclaimController) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&armadav1.ConfigBundle{}, builder.WithPredicates(r.predicate())).
		Named("reclaim").
		Complete(r)
}

func (r *ReclaimController) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	logger := log.FromContext(ctx).WithName("reclaim")

	spec, err := readLastAppliedSpec(ctx, r.Client, r.namespace, req.Name)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("read last-applied-spec: %w", err)
	}
	if spec == nil {
		logger.Info("no last-applied-spec; reclaim deferred to next bundle import", "configbundle", req.Name)
		return reconcile.Result{}, nil
	}

	var cb armadav1.ConfigBundle
	if err := r.Client.Get(ctx, req.NamespacedName, &cb); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	body, err := yaml.Marshal(spec)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("marshal manifest: %w", err)
	}

	logger.Info("replaying last manifest after local:* release",
		"configbundle", req.Name,
		"digest", cb.Status.LastAppliedDigest)

	if err := r.consume.applyManifest(ctx, body, cb.Status.LastAppliedDigest, cb.Status.LastOrbImportID); err != nil {
		return reconcile.Result{}, fmt.Errorf("replay manifest: %w", err)
	}
	return reconcile.Result{}, nil
}

func (r *ReclaimController) predicate() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			return len(localReleasedFieldPaths(e.ObjectOld.GetManagedFields(), e.ObjectNew.GetManagedFields())) > 0
		},
		CreateFunc:  func(_ event.CreateEvent) bool { return false },
		DeleteFunc:  func(_ event.DeleteEvent) bool { return false },
		GenericFunc: func(_ event.GenericEvent) bool { return false },
	}
}

// localReleasedFieldPaths returns the set of FieldsV1-encoded paths that had
// at least one local:* claim in old and zero local:* claims in new.
//
// A "rotation" (local:admin released a field that local:bob claimed in the
// same transaction) does NOT count as a release — at least one local:* manager
// still holds it.
//
// Path strings are FieldsV1 dot-joined (e.g. "f:spec.f:servers.k:{\"orbId\":\"X\"}.f:idrac.f:racadmEnabled").
// They are used only for predicate filtering — no downstream semantic depends
// on the encoding.
func localReleasedFieldPaths(old, new []metav1.ManagedFieldsEntry) []string {
	oldPaths := collectLocalLeafPaths(old)
	newPaths := collectLocalLeafPaths(new)
	var released []string
	for p := range oldPaths {
		if !newPaths[p] {
			released = append(released, p)
		}
	}
	return released
}

// collectLocalLeafPaths flattens all local:* managers' FieldsV1 trees into a
// set of leaf path strings. A leaf is a node whose subtree is empty.
func collectLocalLeafPaths(fields []metav1.ManagedFieldsEntry) map[string]bool {
	paths := map[string]bool{}
	for _, e := range fields {
		if !strings.HasPrefix(e.Manager, "local:") || e.FieldsV1 == nil {
			continue
		}
		var tree map[string]any
		if err := json.Unmarshal(e.FieldsV1.Raw, &tree); err != nil {
			continue
		}
		walkLeaves(tree, "", paths)
	}
	return paths
}

func walkLeaves(node map[string]any, prefix string, out map[string]bool) {
	for k, v := range node {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		sub, ok := v.(map[string]any)
		if !ok || len(sub) == 0 {
			out[path] = true
			continue
		}
		walkLeaves(sub, path, out)
	}
}
