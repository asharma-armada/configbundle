package serverconfig

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeIDRAC stands in for the Dell DellAttributes resource. Tests drive it by
// pre-seeding attributes and then asserting on the recorded PATCH payloads.
type fakeIDRAC struct {
	mu          sync.Mutex
	attributes  map[string]any
	requirePass string
	patchCalls  []map[string]string
}

func newFakeIDRAC(t *testing.T, initial map[string]any, requirePass string) (*httptest.Server, *fakeIDRAC) {
	t.Helper()
	state := &fakeIDRAC{attributes: initial, requirePass: requirePass}
	mux := http.NewServeMux()
	mux.HandleFunc("/redfish/v1/Managers/iDRAC.Embedded.1/Oem/Dell/DellAttributes/iDRAC.Embedded.1", state.handle)
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	return srv, state
}

func (f *fakeIDRAC) handle(w http.ResponseWriter, r *http.Request) {
	user, pass, ok := r.BasicAuth()
	if !ok || user != "root" || pass != f.requirePass {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodGet:
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"@odata.id":   "/redfish/v1/Managers/iDRAC.Embedded.1/Oem/Dell/DellAttributes/iDRAC.Embedded.1",
			"@odata.type": "#DellAttributes.v1_0_0.DellAttributes",
			"Attributes":  f.attributes,
		})
	case http.MethodPatch:
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			Attributes map[string]string `json:"Attributes"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.patchCalls = append(f.patchCalls, payload.Attributes)
		for k, v := range payload.Attributes {
			f.attributes[k] = v
		}
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"@Message.ExtendedInfo":[{"Severity":"OK"}]}`))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func hostFromServer(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	return u.Host
}

func TestRedfishClient_GetAttributes_HappyPath(t *testing.T) {
	srv, _ := newFakeIDRAC(t, map[string]any{
		"SSH.1.Enable":    "Enabled",
		"SSH.1.Port":      22,
		"Racadm.1.Enable": "Enabled",
	}, "sekret")

	c := newRedfishClient(hostFromServer(t, srv), "root", "sekret")
	got, err := c.GetAttributes(context.Background())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got["SSH.1.Enable"] != "Enabled" {
		t.Errorf("expected SSH.1.Enable=Enabled, got %v", got["SSH.1.Enable"])
	}
	// JSON decode turns numbers into float64 — check that we round-trip ints sanely.
	if port, _ := got["SSH.1.Port"].(float64); port != 22 {
		t.Errorf("expected SSH.1.Port=22, got %v", got["SSH.1.Port"])
	}
}

func TestRedfishClient_PatchAttributes_FlipsState(t *testing.T) {
	srv, state := newFakeIDRAC(t, map[string]any{"SSH.1.Enable": "Enabled"}, "sekret")

	c := newRedfishClient(hostFromServer(t, srv), "root", "sekret")
	if err := c.PatchAttributes(context.Background(), map[string]string{"SSH.1.Enable": "Disabled"}); err != nil {
		t.Fatalf("patch: %v", err)
	}

	if state.attributes["SSH.1.Enable"] != "Disabled" {
		t.Errorf("expected fake state to flip to Disabled; got %v", state.attributes["SSH.1.Enable"])
	}
	if len(state.patchCalls) != 1 {
		t.Fatalf("expected exactly 1 PATCH; got %d", len(state.patchCalls))
	}
	if state.patchCalls[0]["SSH.1.Enable"] != "Disabled" {
		t.Errorf("PATCH payload mismatch: %v", state.patchCalls[0])
	}
}

func TestRedfishClient_PatchAttributes_BatchedKeys(t *testing.T) {
	srv, state := newFakeIDRAC(t, map[string]any{
		"SSH.1.Enable":    "Enabled",
		"Racadm.1.Enable": "Enabled",
	}, "sekret")

	c := newRedfishClient(hostFromServer(t, srv), "root", "sekret")
	err := c.PatchAttributes(context.Background(), map[string]string{
		"SSH.1.Enable":    "Disabled",
		"Racadm.1.Enable": "Disabled",
	})
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if len(state.patchCalls) != 1 {
		t.Errorf("multi-key patch must be a single HTTP call; got %d", len(state.patchCalls))
	}
	if state.attributes["SSH.1.Enable"] != "Disabled" || state.attributes["Racadm.1.Enable"] != "Disabled" {
		t.Errorf("expected both keys flipped: %v", state.attributes)
	}
}

func TestRedfishClient_PatchAttributes_EmptyMapIsNoop(t *testing.T) {
	srv, state := newFakeIDRAC(t, map[string]any{"SSH.1.Enable": "Enabled"}, "sekret")

	c := newRedfishClient(hostFromServer(t, srv), "root", "sekret")
	if err := c.PatchAttributes(context.Background(), map[string]string{}); err != nil {
		t.Errorf("empty PATCH should be a no-op, not an error: %v", err)
	}
	if len(state.patchCalls) != 0 {
		t.Errorf("empty PATCH must not hit the network; got %d call(s)", len(state.patchCalls))
	}
}

func TestRedfishClient_PatchAttributes_Idempotent(t *testing.T) {
	// Real iDRAC returns 200 even when value already matches. Our fake follows
	// suit. We're not asserting the fake here; we're asserting the client
	// doesn't error on a no-change PATCH.
	srv, _ := newFakeIDRAC(t, map[string]any{"SSH.1.Enable": "Disabled"}, "sekret")

	c := newRedfishClient(hostFromServer(t, srv), "root", "sekret")
	if err := c.PatchAttributes(context.Background(), map[string]string{"SSH.1.Enable": "Disabled"}); err != nil {
		t.Errorf("idempotent PATCH should succeed: %v", err)
	}
}

func TestRedfishClient_GetAttributes_BadCreds(t *testing.T) {
	srv, _ := newFakeIDRAC(t, map[string]any{"SSH.1.Enable": "Enabled"}, "rightpass")

	c := newRedfishClient(hostFromServer(t, srv), "root", "wrongpass")
	_, err := c.GetAttributes(context.Background())
	if err == nil {
		t.Fatalf("expected auth error, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected 401 in error, got: %v", err)
	}
}

// TestRedfishClient_RetriesTransient503 pins that the retryablehttp wrapper
// retries 5xx and that the client surfaces success after a transient failure.
// One 503 then a 200 — the client should not surface the 503 to the caller.
func TestRedfishClient_RetriesTransient503(t *testing.T) {
	var attempt atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/redfish/v1/Managers/iDRAC.Embedded.1/Oem/Dell/DellAttributes/iDRAC.Embedded.1", func(w http.ResponseWriter, r *http.Request) {
		n := attempt.Add(1)
		user, pass, ok := r.BasicAuth()
		if !ok || user != "root" || pass != "sekret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if n == 1 {
			// First attempt fails with a transient server error.
			http.Error(w, "iDRAC busy", http.StatusServiceUnavailable)
			return
		}
		// Second attempt succeeds.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Attributes": map[string]any{"SSH.1.Enable": "Enabled"},
		})
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	c := newRedfishClient(hostFromServer(t, srv), "root", "sekret")
	got, err := c.GetAttributes(context.Background())
	if err != nil {
		t.Fatalf("expected eventual success after one 503, got: %v", err)
	}
	if got["SSH.1.Enable"] != "Enabled" {
		t.Errorf("expected SSH=Enabled after retry, got %v", got["SSH.1.Enable"])
	}
	if attempt.Load() != 2 {
		t.Errorf("expected exactly 2 attempts (1 retry); got %d", attempt.Load())
	}
}

// TestRedfishClient_DoesNotRetry4xx pins that 4xx errors (e.g. wrong creds)
// are surfaced without retrying — retrying won't fix permission issues.
func TestRedfishClient_DoesNotRetry4xx(t *testing.T) {
	var attempt atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/redfish/v1/Managers/iDRAC.Embedded.1/Oem/Dell/DellAttributes/iDRAC.Embedded.1", func(w http.ResponseWriter, r *http.Request) {
		attempt.Add(1)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	c := newRedfishClient(hostFromServer(t, srv), "root", "anything")
	_, err := c.GetAttributes(context.Background())
	if err == nil {
		t.Fatalf("expected 401 to propagate")
	}
	if attempt.Load() != 1 {
		t.Errorf("4xx must not retry; got %d attempts (expected 1)", attempt.Load())
	}
}
