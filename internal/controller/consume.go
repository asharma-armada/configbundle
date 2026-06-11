package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/yaml"

	armadav1 "github.com/armada/configbundle/api/v1"
	"github.com/armada/configbundle/bundle"
)

const (
	defaultConsumePort = ":8095"
	defaultRetryMax    = 3
	defaultRetryWait   = time.Second
	maxManifestBytes   = 10 * 1024 * 1024 // 10 MB — matches ORBITAL_ENRICHER_MAX_RESPONSE_BYTES
)

// ConsumeServer is a ctrl.Runnable that exposes POST /consume for orb layer dispatch.
// Orb calls this endpoint after pulling and cosign-verifying the OCI artifact, passing
// the raw manifest layer bytes. ConsumeServer validates the payload synchronously and
// applies it to the cluster asynchronously.
type ConsumeServer struct {
	Client    client.Client
	port      string
	namespace string
	retryMax  int
	retryWait time.Duration
	ctx       context.Context // lifecycle context set by Start(); defaults to Background()
	reporter  *DivergenceReporter

	// applyFn overrides applyManifest in tests.
	applyFn func(ctx context.Context, body []byte, digest, importID string) error
}

// ConsumeServerOption configures a ConsumeServer.
type ConsumeServerOption func(*ConsumeServer)

// WithPort sets the TCP address the consume server listens on (default ":8080").
func WithPort(port string) ConsumeServerOption {
	return func(s *ConsumeServer) { s.port = port }
}

// WithNamespace sets the K8s namespace for ConfigBundle CRs (default "configbundle-system").
func WithNamespace(ns string) ConsumeServerOption {
	return func(s *ConsumeServer) { s.namespace = ns }
}

// WithDivergenceReporter links a DivergenceReporter so the consume handler can
// record last-applied manifests for divergence comparison.
func WithDivergenceReporter(r *DivergenceReporter) ConsumeServerOption {
	return func(s *ConsumeServer) { s.reporter = r }
}

// WithRetry configures apply retry. maxAttempts is the total number of attempts
// (1 = no retry). backoffBase is the wait before the second attempt; each subsequent
// attempt doubles it (exponential backoff).
func WithRetry(maxAttempts int, backoffBase time.Duration) ConsumeServerOption {
	return func(s *ConsumeServer) {
		s.retryMax = maxAttempts
		s.retryWait = backoffBase
	}
}

// NewConsumeServer returns a ConsumeServer with sensible defaults.
func NewConsumeServer(c client.Client, opts ...ConsumeServerOption) *ConsumeServer {
	s := &ConsumeServer{
		Client:    c,
		port:      defaultConsumePort,
		namespace: "configbundle-system",
		retryMax:  defaultRetryMax,
		retryWait: defaultRetryWait,
		ctx:       context.Background(),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// NeedsLeaderElection returns false — all replicas serve /consume.
// SSA patches from the same field owner are idempotent; concurrent applies are safe.
func (s *ConsumeServer) NeedsLeaderElection() bool { return false }

// Start implements ctrl.Runnable. Runs until ctx is cancelled.
func (s *ConsumeServer) Start(ctx context.Context) error {
	s.ctx = ctx
	logger := log.FromContext(ctx).WithName("consume-server")

	mux := http.NewServeMux()
	mux.HandleFunc("POST /consume", s.handleConsume)
	srv := &http.Server{Addr: s.port, Handler: mux}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	logger.Info("starting", "port", s.port, "namespace", s.namespace)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("consume server: %w", err)
	}
	return nil
}

// handleConsume is the POST /consume handler. Orb calls this after cosign-verifying
// the artifact and completing its own Dgraph import.
//
// Validation (bad payload) is synchronous and returns 4xx — these are caller errors.
// The K8s apply is asynchronous: the handler returns 200 as soon as the payload is
// accepted, then applies in the background using the server lifecycle context.
// Apply failures surface via ConfigBundle CR status conditions and controller logs,
// not via orb's import history (which is about OCI ingestion, not K8s reconciliation).
func (s *ConsumeServer) handleConsume(w http.ResponseWriter, r *http.Request) {
	logger := log.FromContext(r.Context()).WithName("consume")

	if r.Header.Get("Content-Type") != bundle.MediaTypeManifest {
		http.Error(w, "unsupported media type", http.StatusUnsupportedMediaType)
		return
	}

	tag := r.Header.Get("X-Orb-Tag")
	digest := r.Header.Get("X-Orb-Digest")
	importID := r.Header.Get("X-Orb-Import-ID")

	body, err := io.ReadAll(io.LimitReader(r.Body, maxManifestBytes+1))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if len(body) > maxManifestBytes {
		http.Error(w, "manifest exceeds max size", http.StatusRequestEntityTooLarge)
		return
	}

	// Validate synchronously — bad payload is the caller's concern.
	spec, err := parseManifest(body)
	if err != nil {
		http.Error(w, "invalid manifest: "+err.Error(), http.StatusBadRequest)
		return
	}
	if spec.Datacenter == "" {
		http.Error(w, "manifest missing datacenter field", http.StatusBadRequest)
		return
	}

	logger.Info("received dispatch",
		"tag", tag, "digest", digest, "importID", importID, "bytes", len(body),
	)

	apply := s.applyManifest
	if s.applyFn != nil {
		apply = s.applyFn
	}

	// Apply asynchronously — K8s apply latency must not block orb's import pipeline.
	go func() {
		if err := apply(s.ctx, body, digest, importID); err != nil {
			logger.Error(err, "async apply failed", "importID", importID)
		}
	}()

	w.WriteHeader(http.StatusOK)
}

// applyManifest parses the manifest bytes, runs the admin-override-aware SSA pipeline,
// and updates ConfigBundle status. Retries the SSA patch on transient K8s API errors.
func (s *ConsumeServer) applyManifest(ctx context.Context, body []byte, digest, importID string) error {
	spec, err := parseManifest(body)
	if err != nil {
		return fmt.Errorf("parse manifest: %w", err)
	}
	if spec.Datacenter == "" {
		return fmt.Errorf("manifest has empty datacenter field")
	}

	// Fetch the current CR to read managedFields for omitAdminOwnedServers.
	var cb armadav1.ConfigBundle
	err = s.Client.Get(ctx, types.NamespacedName{
		Name:      spec.Datacenter,
		Namespace: s.namespace,
	}, &cb)
	if client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("get ConfigBundle: %w", err)
	}

	// Omit server entries owned by local:admin to avoid SSA 409 conflicts.
	// With +listType=map, ownership is per-entry by serviceTag.
	patchSpec := omitAdminOwnedServers(spec, cb.ManagedFields)

	apply := &armadav1.ConfigBundle{
		TypeMeta: metav1.TypeMeta{
			APIVersion: armadav1.GroupVersion.String(),
			Kind:       "ConfigBundle",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      spec.Datacenter,
			Namespace: s.namespace,
		},
		Spec: patchSpec,
	}

	// Retry with exponential backoff on transient K8s API errors.
	var lastErr error
	for attempt := 0; attempt < s.retryMax; attempt++ {
		if attempt > 0 {
			wait := s.retryWait * (1 << (attempt - 1)) // 1s, 2s, 4s …
			select {
			case <-ctx.Done():
				return fmt.Errorf("apply cancelled after %d attempt(s): %w", attempt, ctx.Err())
			case <-time.After(wait):
			}
		}
		lastErr = s.Client.Patch(ctx, apply, client.Apply,
			client.FieldOwner("configbundle-controller"),
		)
		if lastErr == nil {
			break
		}
	}
	// Takeover pass — runs regardless of whether the normal apply succeeded (ADR-006).
	// Force-conflicts reclaims ownership of specific fields from local:admin.
	takeoverErr := s.processTakeover(ctx, spec)

	if lastErr != nil && takeoverErr != nil {
		return fmt.Errorf("apply failed: %w; takeover also failed: %v", lastErr, takeoverErr)
	}
	if lastErr != nil {
		return fmt.Errorf("apply ConfigBundle spec (after %d attempt(s)): %w", s.retryMax, lastErr)
	}
	if takeoverErr != nil {
		return fmt.Errorf("takeover: %w", takeoverErr)
	}

	// Record the last-applied manifest for divergence comparison.
	if s.reporter != nil {
		s.reporter.SetLastManifest(spec.Datacenter, spec)
	}

	// After client.Apply, controller-runtime deserialises the full server response back
	// into apply — giving us the current ResourceVersion and existing Status without a
	// second round-trip. A re-fetch via the cached client would race on first-create.
	now := metav1.Now()
	apply.Status.LastAppliedDigest = digest
	apply.Status.LastOrbImportID = importID
	apply.Status.LastAppliedAt = &now
	setCondition(&apply.Status.Conditions, armadav1.ConditionReconciled,
		metav1.ConditionTrue, "Reconciled", "manifest applied via orb dispatch")

	if err := s.Client.Status().Update(ctx, apply); err != nil {
		return fmt.Errorf("update ConfigBundle status: %w", err)
	}

	return nil
}

// parseManifest deserialises the ConfigBundle manifest YAML layer into a ConfigBundleSpec.
func parseManifest(data []byte) (armadav1.ConfigBundleSpec, error) {
	var spec armadav1.ConfigBundleSpec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return spec, fmt.Errorf("unmarshal manifest YAML: %w", err)
	}
	return spec, nil
}

// omitAdminOwnedServers returns a copy of spec with server entries removed if
// local:admin owns any field within that entry. Omitting the entire entry is safe:
// it preserves the admin's full intent and avoids a 409 partial-apply conflict.
func omitAdminOwnedServers(spec armadav1.ConfigBundleSpec, managedFields []metav1.ManagedFieldsEntry) armadav1.ConfigBundleSpec {
	owned := adminOwnedServiceTags(managedFields)
	if len(owned) == 0 {
		return spec
	}
	filtered := make([]armadav1.ServerSpec, 0, len(spec.Servers))
	for _, s := range spec.Servers {
		if !owned[s.ServiceTag] {
			filtered = append(filtered, s)
		}
	}
	spec.Servers = filtered
	return spec
}

// adminOwnedServiceTags parses managedFields and returns the set of serviceTag values
// for server entries that local:admin owns (at any field depth).
//
// With +listType=map +listMapKey=serviceTag, the Kubernetes API encodes per-entry
// ownership in fieldsV1 as:
//
//	{"f:spec": {"f:servers": {"k:{\"serviceTag\":\"3RK3V64\"}": {...}}}}
func adminOwnedServiceTags(managedFields []metav1.ManagedFieldsEntry) map[string]bool {
	owned := map[string]bool{}
	for _, entry := range managedFields {
		if entry.Manager != "local:admin" || entry.FieldsV1 == nil {
			continue
		}
		var fields map[string]interface{}
		if err := json.Unmarshal(entry.FieldsV1.Raw, &fields); err != nil {
			continue
		}
		specFields, _ := fields["f:spec"].(map[string]interface{})
		serverFields, _ := specFields["f:servers"].(map[string]interface{})
		for key := range serverFields {
			if !strings.HasPrefix(key, "k:{") {
				continue
			}
			var keyMap struct {
				ServiceTag string `json:"serviceTag"`
			}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(key, "k:")), &keyMap); err != nil {
				continue
			}
			if keyMap.ServiceTag != "" {
				owned[keyMap.ServiceTag] = true
			}
		}
	}
	return owned
}

// setCondition upserts a metav1.Condition on the conditions slice.
func setCondition(conditions *[]metav1.Condition, condType string, status metav1.ConditionStatus, reason, message string) {
	now := metav1.Now()
	for i, c := range *conditions {
		if c.Type == condType {
			(*conditions)[i].Status = status
			(*conditions)[i].Reason = reason
			(*conditions)[i].Message = message
			(*conditions)[i].LastTransitionTime = now
			return
		}
	}
	*conditions = append(*conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: now,
	})
}
