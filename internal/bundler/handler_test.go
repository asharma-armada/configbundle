package bundler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	armadav1 "github.com/armada/configbundle/api/v1"
	"github.com/armada/configbundle/bundle"
)

type fakeQuerier struct {
	results []DataCenterResult
	err     error
}

func (f *fakeQuerier) QueryDataCenter(_ context.Context, _ string) ([]DataCenterResult, error) {
	return f.results, f.err
}

func newRequest(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/bundle", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestHandleBundle_InvalidJSON(t *testing.T) {
	h := &Handler{Orbital: &fakeQuerier{}}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, newRequest("not json"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHandleBundle_EmptyDatacenter(t *testing.T) {
	h := &Handler{Orbital: &fakeQuerier{}}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, newRequest(`{}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHandleBundle_GraphQLError(t *testing.T) {
	h := &Handler{Orbital: &fakeQuerier{err: fmt.Errorf("connection refused")}}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, newRequest(`{"orbId":"colo:colo"}`))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", w.Code)
	}
}

func TestHandleBundle_DatacenterNotFound(t *testing.T) {
	h := &Handler{Orbital: &fakeQuerier{results: nil}}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, newRequest(`{"orbId":"colo:colo"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	resp := decodeResponse(t, w)
	if len(resp.Layers) != 0 {
		t.Errorf("expected empty layers, got %d", len(resp.Layers))
	}
}

func decodeResponse(t *testing.T, w *httptest.ResponseRecorder) bundleResponse {
	t.Helper()
	var resp bundleResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp
}

func TestHandleBundle_Success(t *testing.T) {
	h := &Handler{Orbital: &fakeQuerier{results: []DataCenterResult{{
		Name:  "colo",
		OrbID: "colo:colo-galleon",
		Servers: []ServerResult{{
			Hostname:   "colo-r740-01",
			ServiceTag: "3RK3V64",
			OrbID:      "colo:srv-001",
			OobIP:      &IPAddressResult{Address: "10.10.1.45"},
			IdracSettings: &IdracSettingsResult{
				OrbID:           "colo:srv-001-idrac",
				FirmwareVersion: "7.20.10.05",
				SSHEnabled:      true,
				IPMIEnabled:     false,
				RacadmEnabled:   true,
			},
		}},
	}}}}

	w := httptest.NewRecorder()
	h.ServeHTTP(w, newRequest(`{"orbId":"colo:colo"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	resp := decodeResponse(t, w)
	layers := resp.Layers
	if len(layers) != 2 {
		t.Fatalf("expected 2 layers, got %d", len(layers))
	}
	if layers[0].MediaType != bundle.MediaTypeManifest {
		t.Errorf("manifest mediaType: got %q", layers[0].MediaType)
	}
	if layers[1].MediaType != bundle.MediaTypeMapping {
		t.Errorf("mapping mediaType: got %q", layers[1].MediaType)
	}

	// Decode and unmarshal to verify round-trip fidelity.
	raw, err := base64.StdEncoding.DecodeString(layers[0].Data)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	var spec armadav1.ConfigBundleSpec
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if spec.Datacenter != "colo" {
		t.Errorf("datacenter: got %q", spec.Datacenter)
	}
	if len(spec.Servers) != 1 {
		t.Fatalf("servers: got %d", len(spec.Servers))
	}
	srv := spec.Servers[0]
	if got := derefString(srv.Hostname); got != "colo-r740-01" {
		t.Errorf("hostname: got %q", got)
	}
	if srv.ServiceTag != "3RK3V64" {
		t.Errorf("serviceTag: got %q", srv.ServiceTag)
	}
	if got := derefString(srv.OobIP); got != "10.10.1.45" {
		t.Errorf("oobIP: got %q", got)
	}
	if got := derefString(srv.Idrac.FirmwareVersion); got != "7.20.10.05" {
		t.Errorf("firmwareVersion: got %q", got)
	}
	if !derefBool(srv.Idrac.SSHEnabled) {
		t.Error("sshEnabled: want true")
	}
	if !derefBool(srv.Idrac.RacadmEnabled) {
		t.Error("racadmEnabled: want true")
	}
}

// --- Unit tests for mapToSpec ---

func TestMapToSpec_SkipsServersWithoutHostname(t *testing.T) {
	dc := DataCenterResult{
		Name:  "colo",
		OrbID: "colo:colo-galleon",
		Servers: []ServerResult{
			{Hostname: "", ServiceTag: "NO-HOST", OrbID: "colo:srv-001"},
			{Hostname: "valid-host", ServiceTag: "HAS-HOST", OrbID: "colo:srv-002"},
		},
	}
	spec := mapToSpec(dc)
	if len(spec.Servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(spec.Servers))
	}
	if got := derefString(spec.Servers[0].Hostname); got != "valid-host" {
		t.Errorf("expected valid-host, got %q", got)
	}
}

func TestMapToSpec_SkipsServersWithoutOrbID(t *testing.T) {
	dc := DataCenterResult{
		Name:  "colo",
		OrbID: "colo:colo-galleon",
		Servers: []ServerResult{
			{Hostname: "no-orbid", ServiceTag: "TAG-A", OrbID: ""},
			{Hostname: "has-orbid", ServiceTag: "TAG-B", OrbID: "colo:srv-002"},
		},
	}
	spec := mapToSpec(dc)
	if len(spec.Servers) != 1 {
		t.Fatalf("expected 1 server (orbid-less skipped), got %d", len(spec.Servers))
	}
	if spec.Servers[0].OrbID != "colo:srv-002" {
		t.Errorf("expected colo:srv-002, got %q", spec.Servers[0].OrbID)
	}
}

func TestMapToSpec_NilOobIP(t *testing.T) {
	dc := DataCenterResult{
		Name:  "colo",
		OrbID: "colo:colo-galleon",
		Servers: []ServerResult{{
			Hostname: "host", ServiceTag: "TAG-A", OrbID: "colo:srv-001", OobIP: nil,
		}},
	}
	spec := mapToSpec(dc)
	if got := derefString(spec.Servers[0].OobIP); got != "" {
		t.Errorf("expected empty oobIP, got %q", got)
	}
}

func TestMapToSpec_NilIdracSettings(t *testing.T) {
	dc := DataCenterResult{
		Name:  "colo",
		OrbID: "colo:colo-galleon",
		Servers: []ServerResult{{
			Hostname: "host", ServiceTag: "TAG-A", OrbID: "colo:srv-001", IdracSettings: nil,
		}},
	}
	spec := mapToSpec(dc)
	idrac := spec.Servers[0].Idrac
	if derefBool(idrac.SSHEnabled) || derefBool(idrac.IPMIEnabled) || derefString(idrac.FirmwareVersion) != "" {
		t.Error("expected zero-value IdracSpec for nil idracSettings")
	}
}

func TestMapToSpec_PopulatesDatacenterOrbID(t *testing.T) {
	dc := DataCenterResult{
		Name:  "colo",
		OrbID: "colo:colo-galleon",
	}
	spec := mapToSpec(dc)
	if spec.OrbID != "colo:colo-galleon" {
		t.Errorf("expected colo:colo-galleon, got %q", spec.OrbID)
	}
	if spec.Datacenter != "colo" {
		t.Errorf("expected colo, got %q", spec.Datacenter)
	}
}

// derefString returns *p or "" if nil.
func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// derefBool returns *p or false if nil.
func derefBool(p *bool) bool {
	if p == nil {
		return false
	}
	return *p
}

// --- Unit tests for buildMapping ---

func TestBuildMapping_FullDatacenter(t *testing.T) {
	dc := DataCenterResult{
		Name:  "colo",
		OrbID: "colo:colo-galleon",
		Servers: []ServerResult{
			{
				Hostname:   "host-01",
				ServiceTag: "3RK3V64",
				OrbID:      "colo:srv-001",
				IdracSettings: &IdracSettingsResult{
					OrbID:           "colo:srv-001-idrac",
					FirmwareVersion: "7.20.10.05",
				},
			},
			{
				Hostname:   "host-02",
				ServiceTag: "7BN2X91",
				OrbID:      "colo:srv-002",
				IdracSettings: &IdracSettingsResult{
					OrbID: "colo:srv-002-idrac",
				},
			},
		},
	}

	mapping := buildMapping(dc)
	// Post-orbId-migration: mapping carries IdracSettings entries only.
	// DC and Server orbIds live in spec directly.
	if len(mapping.Items) != 2 {
		t.Fatalf("expected 2 mapping items (IdracSettings only), got %d: %+v", len(mapping.Items), mapping.Items)
	}

	type want struct {
		orbID string
		typ   string
	}
	expected := map[string]want{
		"spec.servers[orbId=colo:srv-001].idrac": {"colo:srv-001-idrac", "IdracSettings"},
		"spec.servers[orbId=colo:srv-002].idrac": {"colo:srv-002-idrac", "IdracSettings"},
	}

	for _, item := range mapping.Items {
		w, ok := expected[item.Path]
		if !ok {
			t.Errorf("unexpected mapping path %q", item.Path)
			continue
		}
		if item.OrbID != w.orbID {
			t.Errorf("path %q: got orbId %q, want %q", item.Path, item.OrbID, w.orbID)
		}
		if item.Type != w.typ {
			t.Errorf("path %q: got type %q, want %q", item.Path, item.Type, w.typ)
		}
		delete(expected, item.Path)
	}
	for path := range expected {
		t.Errorf("missing expected mapping path %q", path)
	}
}

func TestBuildMapping_SkipsServersWithoutHostname(t *testing.T) {
	dc := DataCenterResult{
		Name:  "colo",
		OrbID: "colo:colo-galleon",
		Servers: []ServerResult{
			{Hostname: "", ServiceTag: "NO-HOST", OrbID: "colo:orphan"},
			{Hostname: "valid", ServiceTag: "HAS-HOST", OrbID: "colo:srv-001"},
		},
	}

	mapping := buildMapping(dc)
	for _, item := range mapping.Items {
		if item.OrbID == "colo:orphan" {
			t.Error("mapping should not include server without hostname")
		}
	}
}

// --- Unit tests for buildTakeover ---

func TestBuildTakeover_Empty(t *testing.T) {
	entries := buildTakeover(nil, MappingLayer{})
	if len(entries) != 0 {
		t.Errorf("expected no entries, got %d", len(entries))
	}
}

func TestBuildTakeover_ResolvesOrbIdToServerOrbID(t *testing.T) {
	mapping := MappingLayer{Items: []MappingEntry{
		{Path: "spec.servers[orbId=colo:srv-001].idrac", OrbID: "colo:srv-001-idrac", Type: "IdracSettings"},
		{Path: "spec.servers[orbId=colo:srv-002].idrac", OrbID: "colo:srv-002-idrac", Type: "IdracSettings"},
	}}

	resolutions := []PendingForceResolution{
		{OrbID: "colo:srv-001-idrac", Field: "sshEnabled"},
		{OrbID: "colo:srv-002-idrac", Field: "racadmEnabled"},
	}

	entries := buildTakeover(resolutions, mapping)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	if entries[0].ServerOrbID != "colo:srv-001" {
		t.Errorf("entry[0] serverOrbId: got %q, want colo:srv-001", entries[0].ServerOrbID)
	}
	if entries[0].Field != "sshEnabled" {
		t.Errorf("entry[0] field: got %q", entries[0].Field)
	}
	if entries[0].OrbID != "colo:srv-001-idrac" {
		t.Errorf("entry[0] orbId: got %q", entries[0].OrbID)
	}

	if entries[1].ServerOrbID != "colo:srv-002" {
		t.Errorf("entry[1] serverOrbId: got %q, want colo:srv-002", entries[1].ServerOrbID)
	}
	if entries[1].Field != "racadmEnabled" {
		t.Errorf("entry[1] field: got %q", entries[1].Field)
	}
}

func TestBuildTakeover_SkipsUnknownOrbId(t *testing.T) {
	mapping := MappingLayer{Items: []MappingEntry{
		{Path: "spec.servers[orbId=colo:srv-001].idrac", OrbID: "colo:srv-001-idrac", Type: "IdracSettings"},
	}}

	resolutions := []PendingForceResolution{
		{OrbID: "colo:srv-001-idrac", Field: "sshEnabled"},
		{OrbID: "unknown:orb-id", Field: "hostname"},
	}

	entries := buildTakeover(resolutions, mapping)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry (unknown skipped), got %d", len(entries))
	}
}

// --- Unit tests for extractServerOrbID ---

func TestExtractServerOrbID(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"spec.servers[orbId=colo:srv-001]", "colo:srv-001"},
		{"spec.servers[orbId=colo:srv-001].idrac", "colo:srv-001"},
		{"spec.servers[orbId=colo:srv-001].idrac.sshEnabled", "colo:srv-001"},
		{"spec", ""},
		{"spec.datacenter", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := extractServerOrbID(tt.path)
			if got != tt.want {
				t.Errorf("extractServerOrbID(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

// --- Handler test with resolutions ---

type fakeResolutionQuerier struct {
	resolutions []PendingForceResolution
	omissions   []Omission
	err         error
	omErr       error
}

func (f *fakeResolutionQuerier) QueryPendingForce(_ context.Context) ([]PendingForceResolution, error) {
	return f.resolutions, f.err
}

func (f *fakeResolutionQuerier) QueryOmissions(_ context.Context) ([]Omission, error) {
	return f.omissions, f.omErr
}

func TestHandleBundle_WithTakeover(t *testing.T) {
	h := &Handler{
		Orbital: &fakeQuerier{results: []DataCenterResult{{
			Name:  "colo",
			OrbID: "colo:colo-galleon",
			Servers: []ServerResult{{
				Hostname:   "host-01",
				ServiceTag: "3RK3V64",
				OrbID:      "colo:srv-001",
				OobIP:      &IPAddressResult{Address: "10.10.1.45"},
				IdracSettings: &IdracSettingsResult{
					OrbID:      "colo:srv-001-idrac",
					SSHEnabled: true,
				},
			}},
		}}},
		Resolutions: &fakeResolutionQuerier{resolutions: []PendingForceResolution{
			{OrbID: "colo:srv-001-idrac", Field: "sshEnabled"},
		}},
	}

	w := httptest.NewRecorder()
	h.ServeHTTP(w, newRequest(`{"orbId":"colo:colo"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	resp := decodeResponse(t, w)

	// Verify manifest contains takeover entry
	raw, err := base64.StdEncoding.DecodeString(resp.Layers[0].Data)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	var spec armadav1.ConfigBundleSpec
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if len(spec.Takeover) != 1 {
		t.Fatalf("expected 1 takeover entry, got %d", len(spec.Takeover))
	}
	if spec.Takeover[0].ServerOrbID != "colo:srv-001" {
		t.Errorf("takeover serverOrbId: got %q", spec.Takeover[0].ServerOrbID)
	}
	if spec.Takeover[0].Field != "sshEnabled" {
		t.Errorf("takeover field: got %q", spec.Takeover[0].Field)
	}
	if spec.Takeover[0].OrbID != "colo:srv-001-idrac" {
		t.Errorf("takeover orbId: got %q", spec.Takeover[0].OrbID)
	}
}

func TestHandleBundle_ResolutionQueryError(t *testing.T) {
	h := &Handler{
		Orbital: &fakeQuerier{results: []DataCenterResult{{
			Name:    "colo",
			Servers: []ServerResult{{Hostname: "host-01", ServiceTag: "3RK3V64"}},
		}}},
		Resolutions: &fakeResolutionQuerier{err: fmt.Errorf("connection refused")},
	}

	w := httptest.NewRecorder()
	h.ServeHTTP(w, newRequest(`{"orbId":"colo:colo"}`))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", w.Code)
	}
}

func TestBuildMapping_ServerWithoutOrbIdSkipped(t *testing.T) {
	// Servers without an orbId are skipped entirely — they can't be encoded
	// as a path because the path uses orbId as the list key.
	dc := DataCenterResult{
		Name:  "colo",
		OrbID: "colo:colo-galleon",
		Servers: []ServerResult{
			{
				Hostname:   "host-01",
				ServiceTag: "3RK3V64",
				// No OrbID on server → skipped
				IdracSettings: &IdracSettingsResult{
					OrbID: "colo:srv-001-idrac",
				},
			},
		},
	}

	mapping := buildMapping(dc)
	if len(mapping.Items) != 0 {
		t.Fatalf("expected 0 mapping items for server without orbId, got %d: %+v", len(mapping.Items), mapping.Items)
	}
}

// --- Unit tests for applyOmissions ---

func TestApplyOmissions_NoOp(t *testing.T) {
	spec := armadav1.ConfigBundleSpec{
		Datacenter: "colo",
		Servers: []armadav1.ServerSpec{{
			OrbID:    "colo:srv-001",
			Hostname: ptrString("host-01"),
			Idrac:    armadav1.IdracSpec{SSHEnabled: ptrBool(true)},
		}},
	}
	applyOmissions(&spec, nil, MappingLayer{})
	if spec.Servers[0].Idrac.SSHEnabled == nil || *spec.Servers[0].Idrac.SSHEnabled != true {
		t.Errorf("nil omissions must not touch the spec")
	}
}

func TestApplyOmissions_ZeroesIdracLeaf(t *testing.T) {
	spec := armadav1.ConfigBundleSpec{
		Datacenter: "colo",
		Servers: []armadav1.ServerSpec{{
			OrbID:    "colo:srv-001",
			Hostname: ptrString("host-01"),
			Idrac: armadav1.IdracSpec{
				SSHEnabled:    ptrBool(true),
				RacadmEnabled: ptrBool(true),
			},
		}},
	}
	mapping := MappingLayer{Items: []MappingEntry{
		{Path: "spec.servers[orbId=colo:srv-001].idrac", OrbID: "colo:srv-001-idrac", Type: "IdracSettings"},
	}}
	omissions := []Omission{
		{OrbID: "colo:srv-001-idrac", Field: "sshEnabled"},
	}

	applyOmissions(&spec, omissions, mapping)

	if spec.Servers[0].Idrac.SSHEnabled != nil {
		t.Errorf("sshEnabled must be nil after omission, got %v", *spec.Servers[0].Idrac.SSHEnabled)
	}
	if spec.Servers[0].Idrac.RacadmEnabled == nil || *spec.Servers[0].Idrac.RacadmEnabled != true {
		t.Errorf("racadmEnabled must be untouched (not in omission list)")
	}
}

func TestApplyOmissions_OmittedFieldIsAbsentFromYAML(t *testing.T) {
	// The whole point: after applyOmissions, the marshaled YAML must NOT contain
	// the omitted field. This is what releases cb-controller's claim under SSA.
	spec := armadav1.ConfigBundleSpec{
		OrbID:      "colo:colo-galleon",
		Datacenter: "colo",
		Servers: []armadav1.ServerSpec{{
			OrbID:    "colo:srv-001",
			Hostname: ptrString("host-01"),
			Idrac: armadav1.IdracSpec{
				SSHEnabled:    ptrBool(true),
				RacadmEnabled: ptrBool(true),
			},
		}},
	}
	mapping := MappingLayer{Items: []MappingEntry{
		{Path: "spec.servers[orbId=colo:srv-001].idrac", OrbID: "colo:srv-001-idrac", Type: "IdracSettings"},
	}}
	applyOmissions(&spec, []Omission{
		{OrbID: "colo:srv-001-idrac", Field: "sshEnabled"},
	}, mapping)

	out, err := yaml.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	yamlStr := string(out)
	if strings.Contains(yamlStr, "sshEnabled") {
		t.Errorf("omitted field still appears in YAML — omitempty not honored or filter broken.\nYAML:\n%s", yamlStr)
	}
	if !strings.Contains(yamlStr, "racadmEnabled") {
		t.Errorf("non-omitted field missing from YAML.\nYAML:\n%s", yamlStr)
	}
}

func TestApplyOmissions_UnknownOrbIdSkipped(t *testing.T) {
	spec := armadav1.ConfigBundleSpec{
		Servers: []armadav1.ServerSpec{{
			OrbID:    "colo:srv-001",
			Hostname: ptrString("host-01"),
			Idrac:    armadav1.IdracSpec{SSHEnabled: ptrBool(true)},
		}},
	}
	mapping := MappingLayer{} // empty — no resolution possible
	applyOmissions(&spec, []Omission{
		{OrbID: "unknown:srv-001-idrac", Field: "sshEnabled"},
	}, mapping)
	if spec.Servers[0].Idrac.SSHEnabled == nil {
		t.Errorf("unknown orbId omission must not zero the field")
	}
}

func TestApplyOmissions_ServerLevelField(t *testing.T) {
	// Omission can target Hostname (ServerSpec-level) too. We rely on the mapping
	// path encoding to derive the server orbId, then nil the field.
	spec := armadav1.ConfigBundleSpec{
		Servers: []armadav1.ServerSpec{{
			OrbID:    "colo:srv-001",
			Hostname: ptrString("host-01"),
			OobIP:    ptrString("10.0.0.1"),
		}},
	}
	// Mapping path encoded as if a Server-level orbital node owns the hostname.
	// In practice the server itself is the owner; the mapping references its orbId.
	mapping := MappingLayer{Items: []MappingEntry{
		{Path: "spec.servers[orbId=colo:srv-001]", OrbID: "colo:srv-001", Type: "Server"},
	}}
	applyOmissions(&spec, []Omission{
		{OrbID: "colo:srv-001", Field: "hostname"},
	}, mapping)

	if spec.Servers[0].Hostname != nil {
		t.Errorf("hostname must be nil after omission, got %q", *spec.Servers[0].Hostname)
	}
	if spec.Servers[0].OobIP == nil || *spec.Servers[0].OobIP != "10.0.0.1" {
		t.Errorf("oobIP must be untouched")
	}
}

func ptrString(s string) *string { return &s }
func ptrBool(b bool) *bool       { return &b }
