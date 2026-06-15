package controller

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/armada/configbundle/bundle"
)

// --- Unit tests for handleConsume (manifest path via handleDispatch) ---

func TestHandleConsume_WrongMediaType(t *testing.T) {
	s := NewConsumeServer(nil)
	req := httptest.NewRequest(http.MethodPost, "/consume", bytes.NewBufferString("body"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handleConsume(w, req)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415, got %d", w.Code)
	}
}

func TestHandleConsume_OversizedBody(t *testing.T) {
	s := NewConsumeServer(nil)
	big := bytes.Repeat([]byte("x"), maxManifestBytes+1)
	req := httptest.NewRequest(http.MethodPost, "/consume", bytes.NewReader(big))
	req.Header.Set("Content-Type", bundle.MediaTypeManifest)
	w := httptest.NewRecorder()
	s.handleConsume(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", w.Code)
	}
}

func TestHandleConsume_InvalidManifest(t *testing.T) {
	s := NewConsumeServer(nil)
	req := httptest.NewRequest(http.MethodPost, "/consume", bytes.NewBufferString(":\tbad yaml"))
	req.Header.Set("Content-Type", bundle.MediaTypeManifest)
	w := httptest.NewRecorder()
	s.handleConsume(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHandleConsume_EmptyDatacenter(t *testing.T) {
	s := NewConsumeServer(nil)
	req := httptest.NewRequest(http.MethodPost, "/consume", bytes.NewBufferString("servers: []"))
	req.Header.Set("Content-Type", bundle.MediaTypeManifest)
	w := httptest.NewRecorder()
	s.handleConsume(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHandleConsume_ApplyFnError(t *testing.T) {
	s := NewConsumeServer(nil)
	done := make(chan struct{})
	s.applyFn = func(_ context.Context, _ []byte, _, _ string) error {
		defer close(done)
		return fmt.Errorf("k8s unavailable")
	}
	req := httptest.NewRequest(http.MethodPost, "/consume", bytes.NewBufferString("datacenter: colo"))
	req.Header.Set("Content-Type", bundle.MediaTypeManifest)
	w := httptest.NewRecorder()
	s.handleConsume(w, req)
	// Apply errors do not affect the HTTP response — they surface via CR status conditions.
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	<-done // wait for async apply
}

func TestHandleConsume_Success(t *testing.T) {
	s := NewConsumeServer(nil)
	var gotBody []byte
	var gotDigest, gotImportID string
	done := make(chan struct{})
	s.applyFn = func(_ context.Context, body []byte, digest, importID string) error {
		gotBody = body
		gotDigest = digest
		gotImportID = importID
		close(done)
		return nil
	}
	body := []byte("datacenter: colo")
	req := httptest.NewRequest(http.MethodPost, "/consume", bytes.NewReader(body))
	req.Header.Set("Content-Type", bundle.MediaTypeManifest)
	req.Header.Set("X-Orb-Digest", "sha256:abc")
	req.Header.Set("X-Orb-Import-ID", "uuid-123")
	w := httptest.NewRecorder()
	s.handleConsume(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	<-done // wait for async apply
	if string(gotBody) != string(body) {
		t.Errorf("body mismatch: got %q", gotBody)
	}
	if gotDigest != "sha256:abc" {
		t.Errorf("digest mismatch: got %q", gotDigest)
	}
	if gotImportID != "uuid-123" {
		t.Errorf("importID mismatch: got %q", gotImportID)
	}
}

// --- handleDispatch routing tests ---

func TestHandleDispatch_UnsupportedMediaType(t *testing.T) {
	s := NewConsumeServer(nil)
	req := httptest.NewRequest(http.MethodPost, "/dispatch", bytes.NewBufferString("{}"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handleDispatch(w, req)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415, got %d", w.Code)
	}
}

func TestHandleDispatch_MappingMissingDigest(t *testing.T) {
	s := NewConsumeServer(nil)
	req := httptest.NewRequest(http.MethodPost, "/dispatch", bytes.NewBufferString(`{"items":[]}`))
	req.Header.Set("Content-Type", bundle.MediaTypeMapping)
	// No X-Orb-Digest header
	w := httptest.NewRecorder()
	s.handleDispatch(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHandleDispatch_ManifestRouted(t *testing.T) {
	s := NewConsumeServer(nil)
	done := make(chan struct{})
	s.applyFn = func(_ context.Context, _ []byte, _, _ string) error {
		close(done)
		return nil
	}
	req := httptest.NewRequest(http.MethodPost, "/dispatch", bytes.NewBufferString("datacenter: colo"))
	req.Header.Set("Content-Type", bundle.MediaTypeManifest)
	req.Header.Set("X-Orb-Digest", "sha256:abc")
	w := httptest.NewRecorder()
	s.handleDispatch(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	<-done // wait for async apply to confirm routing reached the manifest handler
}

// --- Unit tests for applyManifest helpers ---

func TestParseManifest(t *testing.T) {
	cases := []struct {
		name        string
		input       []byte
		wantDC      string
		wantServers int
		wantErr     bool
	}{
		{
			name:        "valid single server",
			input:       []byte("datacenter: colo\nservers:\n- serviceTag: 3RK3V64\n  hostname: r740\n  oobIP: 10.0.0.1\n"),
			wantDC:      "colo",
			wantServers: 1,
		},
		{
			name:        "no servers",
			input:       []byte("datacenter: colo\n"),
			wantDC:      "colo",
			wantServers: 0,
		},
		{
			name:    "invalid YAML",
			input:   []byte(":\tbad yaml\n"),
			wantErr: true,
		},
		{
			name:        "empty input",
			input:       []byte{},
			wantDC:      "",
			wantServers: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := parseManifest(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if spec.Datacenter != tc.wantDC {
				t.Errorf("datacenter: want %q got %q", tc.wantDC, spec.Datacenter)
			}
			if len(spec.Servers) != tc.wantServers {
				t.Errorf("servers: want %d got %d", tc.wantServers, len(spec.Servers))
			}
		})
	}
}
