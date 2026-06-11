package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	armadav1 "github.com/armada/configbundle/api/v1"
)

// DivergenceReporter is a ctrl.Runnable that periodically inspects ConfigBundle CRs
// for fields owned by local:admin and reports them to orb's divergence intake.
type DivergenceReporter struct {
	Client     client.Client
	HTTPClient *http.Client
	intakeURL  string
	namespace  string
	interval   time.Duration
	enabled    bool

	// lastManifests caches the last-applied manifest spec per ConfigBundle name
	// so we can report intendedValue without re-fetching from anywhere.
	lastManifests map[string]armadav1.ConfigBundleSpec
}

// DivergenceReporterOption configures a DivergenceReporter.
type DivergenceReporterOption func(*DivergenceReporter)

func WithDivergenceIntakeURL(url string) DivergenceReporterOption {
	return func(r *DivergenceReporter) { r.intakeURL = url }
}

func WithDivergenceNamespace(ns string) DivergenceReporterOption {
	return func(r *DivergenceReporter) { r.namespace = ns }
}

func WithDivergenceInterval(d time.Duration) DivergenceReporterOption {
	return func(r *DivergenceReporter) { r.interval = d }
}

func WithDivergenceEnabled(enabled bool) DivergenceReporterOption {
	return func(r *DivergenceReporter) { r.enabled = enabled }
}

func NewDivergenceReporter(c client.Client, opts ...DivergenceReporterOption) *DivergenceReporter {
	r := &DivergenceReporter{
		Client:        c,
		HTTPClient:    &http.Client{Timeout: 10 * time.Second},
		intakeURL:     "http://orb:8010/api/v1/divergence",
		namespace:     "configbundle-system",
		interval:      5 * time.Minute,
		enabled:       false,
		lastManifests: make(map[string]armadav1.ConfigBundleSpec),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

func (r *DivergenceReporter) NeedsLeaderElection() bool { return true }

func (r *DivergenceReporter) Start(ctx context.Context) error {
	if !r.enabled {
		log.FromContext(ctx).WithName("divergence-reporter").Info("disabled, not starting")
		return nil
	}

	logger := log.FromContext(ctx).WithName("divergence-reporter")
	logger.Info("starting", "interval", r.interval, "intakeURL", r.intakeURL)

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := r.report(ctx); err != nil {
				logger.Error(err, "report tick failed")
			}
		}
	}
}

// OverrideEntry is one divergence entry in the intake payload.
type OverrideEntry struct {
	Path          string      `json:"path"`
	IntendedValue interface{} `json:"intendedValue"`
	OverrideValue interface{} `json:"overrideValue"`
	Who           string      `json:"who"`
	When          string      `json:"when"`
}

// DivergencePayload is the full intake payload sent to orb.
type DivergencePayload struct {
	BundleDigest string          `json:"bundleDigest"`
	Overrides    []OverrideEntry `json:"overrides"`
}

func (r *DivergenceReporter) report(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("divergence-reporter")

	var cbList armadav1.ConfigBundleList
	if err := r.Client.List(ctx, &cbList, client.InNamespace(r.namespace)); err != nil {
		return fmt.Errorf("list ConfigBundles: %w", err)
	}

	for i := range cbList.Items {
		cb := &cbList.Items[i]
		overrides := r.extractOverrides(cb)
		payload := DivergencePayload{
			BundleDigest: cb.Status.LastAppliedDigest,
			Overrides:    overrides,
		}

		if err := r.postToOrb(ctx, payload); err != nil {
			logger.Error(err, "failed to POST divergence", "configbundle", cb.Name)
			continue
		}
		logger.Info("reported divergence", "configbundle", cb.Name, "overrides", len(overrides))
	}
	return nil
}

func (r *DivergenceReporter) postToOrb(ctx context.Context, payload DivergencePayload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.intakeURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("POST divergence: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("orb returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// extractOverrides walks managedFields on the ConfigBundle CR, finds fields
// owned by local:admin, and produces OverrideEntry values with K8s field paths.
func (r *DivergenceReporter) extractOverrides(cb *armadav1.ConfigBundle) []OverrideEntry {
	adminPaths := extractAdminPaths(cb.ManagedFields)
	if len(adminPaths) == 0 {
		return nil
	}

	var overrides []OverrideEntry
	for _, ap := range adminPaths {
		intended := resolveValue(r.lastManifests[cb.Name], ap.path)
		override := resolveValue(cb.Spec, ap.path)

		if reflect.DeepEqual(intended, override) {
			continue
		}

		overrides = append(overrides, OverrideEntry{
			Path:          ap.path,
			IntendedValue: intended,
			OverrideValue: override,
			Who:           "local:admin",
			When:          ap.when.Format(time.RFC3339),
		})
	}
	return overrides
}

// SetLastManifest records the last-applied manifest for a ConfigBundle so the
// reporter can compare current values against intended values.
func (r *DivergenceReporter) SetLastManifest(name string, spec armadav1.ConfigBundleSpec) {
	r.lastManifests[name] = spec
}

type adminPath struct {
	path string
	when time.Time
}

// extractAdminPaths parses managedFields to find all leaf field paths owned by local:admin.
// Paths are formatted as: spec.servers[serviceTag=X].idrac.sshEnabled
func extractAdminPaths(managedFields []metav1.ManagedFieldsEntry) []adminPath {
	var paths []adminPath
	for _, entry := range managedFields {
		if entry.Manager != "local:admin" || entry.FieldsV1 == nil {
			continue
		}
		when := time.Time{}
		if entry.Time != nil {
			when = entry.Time.Time
		}

		var fields map[string]interface{}
		if err := json.Unmarshal(entry.FieldsV1.Raw, &fields); err != nil {
			continue
		}

		walkFields(fields, "", when, &paths)
	}
	return paths
}

// walkFields recursively walks the fieldsV1 structure and emits leaf paths.
func walkFields(node map[string]interface{}, prefix string, when time.Time, out *[]adminPath) {
	for key, val := range node {
		path := fieldKeyToPath(prefix, key)
		if path == "" {
			continue
		}

		child, ok := val.(map[string]interface{})
		if !ok || len(child) == 0 {
			*out = append(*out, adminPath{path: path, when: when})
			continue
		}

		// Check if all children are leaf markers (empty maps or non-maps).
		// If so, this is still a leaf set by the manager.
		hasSubfields := false
		for _, v := range child {
			if m, ok := v.(map[string]interface{}); ok && len(m) > 0 {
				hasSubfields = true
				break
			}
		}
		if hasSubfields {
			walkFields(child, path, when, out)
		} else {
			// Leaf — all children are empty maps (field markers).
			for childKey := range child {
				leafPath := fieldKeyToPath(path, childKey)
				if leafPath != "" {
					*out = append(*out, adminPath{path: leafPath, when: when})
				}
			}
		}
	}
}

// fieldKeyToPath converts a fieldsV1 key (e.g. "f:hostname", "k:{\"serviceTag\":\"X\"}")
// into a dot-separated path segment appended to prefix.
func fieldKeyToPath(prefix, key string) string {
	switch {
	case strings.HasPrefix(key, "f:"):
		field := strings.TrimPrefix(key, "f:")
		if prefix == "" {
			return field
		}
		return prefix + "." + field
	case strings.HasPrefix(key, "k:"):
		// Map key — e.g. k:{"serviceTag":"3RK3V64"}
		raw := strings.TrimPrefix(key, "k:")
		var keyMap map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &keyMap); err != nil {
			return ""
		}
		// Build selector like [serviceTag=3RK3V64]
		for k, v := range keyMap {
			selector := fmt.Sprintf("[%s=%v]", k, v)
			if prefix == "" {
				return selector
			}
			return prefix + selector
		}
		return ""
	default:
		return ""
	}
}

// resolveValue reads a value from a ConfigBundleSpec given a K8s field path.
// Paths start with "spec." (from managedFields) — the prefix is stripped since
// the caller passes the spec object directly.
// Returns nil if the path cannot be resolved.
func resolveValue(spec armadav1.ConfigBundleSpec, path string) interface{} {
	path = strings.TrimPrefix(path, "spec.")
	parts := splitPath(path)
	if len(parts) == 0 {
		return nil
	}

	var current interface{} = spec
	for _, part := range parts {
		if part.selector != "" {
			// Array lookup by map key
			servers, ok := current.([]armadav1.ServerSpec)
			if !ok {
				// Try to access via reflection for the field before the selector
				return nil
			}
			found := false
			for _, s := range servers {
				if s.ServiceTag == part.selector {
					current = s
					found = true
					break
				}
			}
			if !found {
				return nil
			}
			continue
		}

		// Field access via JSON name matching
		current = getFieldByJSONName(current, part.field)
		if current == nil {
			return nil
		}
	}
	return current
}

type pathPart struct {
	field    string
	selector string // e.g. "3RK3V64" for [serviceTag=3RK3V64]
}

// splitPath splits a K8s field path into parts.
// "spec.servers[serviceTag=X].idrac.sshEnabled" →
// [{field:"spec"}, {field:"servers", selector:"X"}, {field:"idrac"}, {field:"sshEnabled"}]
func splitPath(path string) []pathPart {
	var parts []pathPart
	remaining := path
	for remaining != "" {
		// Find next dot or bracket
		dotIdx := strings.Index(remaining, ".")
		bracketIdx := strings.Index(remaining, "[")

		if bracketIdx >= 0 && (dotIdx < 0 || bracketIdx < dotIdx) {
			// There's a bracket before the next dot
			field := remaining[:bracketIdx]
			if field != "" {
				parts = append(parts, pathPart{field: field})
			}
			// Parse the selector
			endBracket := strings.Index(remaining, "]")
			if endBracket < 0 {
				break
			}
			selectorStr := remaining[bracketIdx+1 : endBracket]
			// Parse "serviceTag=X"
			eqIdx := strings.Index(selectorStr, "=")
			if eqIdx >= 0 {
				parts = append(parts, pathPart{selector: selectorStr[eqIdx+1:]})
			}
			remaining = remaining[endBracket+1:]
			if strings.HasPrefix(remaining, ".") {
				remaining = remaining[1:]
			}
		} else if dotIdx >= 0 {
			field := remaining[:dotIdx]
			if field != "" {
				parts = append(parts, pathPart{field: field})
			}
			remaining = remaining[dotIdx+1:]
		} else {
			if remaining != "" {
				parts = append(parts, pathPart{field: remaining})
			}
			break
		}
	}
	return parts
}

// getFieldByJSONName returns the value of a struct field matched by its json tag name.
func getFieldByJSONName(obj interface{}, name string) interface{} {
	v := reflect.ValueOf(obj)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil
	}
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		jsonName := strings.Split(tag, ",")[0]
		if jsonName == name {
			return v.Field(i).Interface()
		}
	}
	return nil
}
