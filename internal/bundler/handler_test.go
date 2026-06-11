package bundler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	h.ServeHTTP(w, newRequest(`{"datacenter":"colo"}`))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", w.Code)
	}
}

func TestHandleBundle_DatacenterNotFound(t *testing.T) {
	h := &Handler{Orbital: &fakeQuerier{results: nil}}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, newRequest(`{"datacenter":"colo"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var layers []bundleLayer
	if err := json.NewDecoder(w.Body).Decode(&layers); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(layers) != 0 {
		t.Errorf("expected empty layers, got %d", len(layers))
	}
}

func TestHandleBundle_Success(t *testing.T) {
	h := &Handler{Orbital: &fakeQuerier{results: []DataCenterResult{{
		Name: "colo",
		Servers: []ServerResult{{
			Hostname:   "colo-r740-01",
			ServiceTag: "3RK3V64",
			OobIP:      &IPAddressResult{Address: "10.10.1.45"},
			IdracSettings: &IdracSettingsResult{
				FirmwareVersion: "7.20.10.05",
				SSHEnabled:      true,
				IPMIEnabled:     false,
				RacadmEnabled:   true,
			},
		}},
	}}}}

	w := httptest.NewRecorder()
	h.ServeHTTP(w, newRequest(`{"datacenter":"colo"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var layers []bundleLayer
	if err := json.NewDecoder(w.Body).Decode(&layers); err != nil {
		t.Fatalf("decode response: %v", err)
	}
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
	if srv.Hostname != "colo-r740-01" {
		t.Errorf("hostname: got %q", srv.Hostname)
	}
	if srv.ServiceTag != "3RK3V64" {
		t.Errorf("serviceTag: got %q", srv.ServiceTag)
	}
	if srv.OobIP != "10.10.1.45" {
		t.Errorf("oobIP: got %q", srv.OobIP)
	}
	if srv.Idrac.FirmwareVersion != "7.20.10.05" {
		t.Errorf("firmwareVersion: got %q", srv.Idrac.FirmwareVersion)
	}
	if !srv.Idrac.SSHEnabled {
		t.Error("sshEnabled: want true")
	}
	if !srv.Idrac.RacadmEnabled {
		t.Error("racadmEnabled: want true")
	}
}

// --- Unit tests for mapToSpec ---

func TestMapToSpec_SkipsServersWithoutHostname(t *testing.T) {
	dc := DataCenterResult{
		Name: "colo",
		Servers: []ServerResult{
			{Hostname: "", ServiceTag: "NO-HOST"},
			{Hostname: "valid-host", ServiceTag: "HAS-HOST"},
		},
	}
	spec := mapToSpec(dc)
	if len(spec.Servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(spec.Servers))
	}
	if spec.Servers[0].Hostname != "valid-host" {
		t.Errorf("expected valid-host, got %q", spec.Servers[0].Hostname)
	}
}

func TestMapToSpec_NilOobIP(t *testing.T) {
	dc := DataCenterResult{
		Name:    "colo",
		Servers: []ServerResult{{Hostname: "host", OobIP: nil}},
	}
	spec := mapToSpec(dc)
	if spec.Servers[0].OobIP != "" {
		t.Errorf("expected empty oobIP, got %q", spec.Servers[0].OobIP)
	}
}

func TestMapToSpec_NilIdracSettings(t *testing.T) {
	dc := DataCenterResult{
		Name:    "colo",
		Servers: []ServerResult{{Hostname: "host", IdracSettings: nil}},
	}
	spec := mapToSpec(dc)
	// Zero-value IdracSpec — all bools false, firmware empty.
	idrac := spec.Servers[0].Idrac
	if idrac.SSHEnabled || idrac.IPMIEnabled || idrac.FirmwareVersion != "" {
		t.Error("expected zero-value IdracSpec for nil idracSettings")
	}
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
	if len(mapping.Items) != 5 {
		t.Fatalf("expected 5 mapping items, got %d: %+v", len(mapping.Items), mapping.Items)
	}

	type want struct {
		orbID string
		typ   string
	}
	expected := map[string]want{
		"spec":                               {"colo:colo-galleon", "DataCenter"},
		"spec.servers[serviceTag=3RK3V64]":       {"colo:srv-001", "Server"},
		"spec.servers[serviceTag=3RK3V64].idrac": {"colo:srv-001-idrac", "IdracSettings"},
		"spec.servers[serviceTag=7BN2X91]":       {"colo:srv-002", "Server"},
		"spec.servers[serviceTag=7BN2X91].idrac": {"colo:srv-002-idrac", "IdracSettings"},
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

func TestBuildMapping_MissingOrbIds(t *testing.T) {
	dc := DataCenterResult{
		Name: "colo",
		// No OrbID on datacenter
		Servers: []ServerResult{
			{
				Hostname:   "host-01",
				ServiceTag: "3RK3V64",
				// No OrbID on server
				IdracSettings: &IdracSettingsResult{
					OrbID: "colo:srv-001-idrac",
				},
			},
		},
	}

	mapping := buildMapping(dc)
	// Should only have the idrac entry (datacenter and server have no orbId)
	if len(mapping.Items) != 1 {
		t.Fatalf("expected 1 mapping item, got %d: %+v", len(mapping.Items), mapping.Items)
	}
	if mapping.Items[0].OrbID != "colo:srv-001-idrac" {
		t.Errorf("got orbId %q", mapping.Items[0].OrbID)
	}
}
