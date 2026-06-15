package bundler

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// newFakeTokenServer returns a test server that issues minimal OAuth2 tokens.
func newFakeTokenServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"test-access-token","token_type":"Bearer","expires_in":3600}`))
	}))
}

// newTestOAuth2Client builds an OAuth2 client pointed at a fake token server.
func newTestOAuth2Client(t *testing.T, tokenSrv *httptest.Server) *http.Client {
	t.Helper()
	cfg := &Config{
		OIDCIssuerURL:    tokenSrv.URL, // token URL will be derived as tokenSrv.URL/oauth2/v2.0/token
		OIDCClientID:     "test-client",
		OIDCClientSecret: "test-secret",
	}
	return newOAuth2HTTPClientWithURL(cfg, tokenSrv.URL+"/token")
}

func TestStaticBearerTransport_SetsHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewStaticBearerHTTPClient("my-token")
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()

	if gotAuth != "Bearer my-token" {
		t.Errorf("Authorization header: got %q, want %q", gotAuth, "Bearer my-token")
	}
}

func TestRetryOn401Transport_NoRetryOn200(t *testing.T) {
	var calls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer api.Close()

	tokenSrv := newFakeTokenServer(t)
	defer tokenSrv.Close()

	client := newTestOAuth2Client(t, tokenSrv)
	resp, err := client.Get(api.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()

	if calls.Load() != 1 {
		t.Errorf("expected 1 API call, got %d", calls.Load())
	}
}

func TestRetryOn401Transport_RetryOn401(t *testing.T) {
	var calls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer api.Close()

	tokenSrv := newFakeTokenServer(t)
	defer tokenSrv.Close()

	client := newTestOAuth2Client(t, tokenSrv)
	resp, err := client.Get(api.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 after retry, got %d", resp.StatusCode)
	}
	if calls.Load() != 2 {
		t.Errorf("expected 2 API calls (1 original + 1 retry), got %d", calls.Load())
	}
}

func TestRetryOn401Transport_NoSecondRetryAfterRetry(t *testing.T) {
	// If the retry also returns 401, return it as-is (no infinite loop).
	var calls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer api.Close()

	tokenSrv := newFakeTokenServer(t)
	defer tokenSrv.Close()

	client := newTestOAuth2Client(t, tokenSrv)
	resp, err := client.Get(api.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 returned after retry, got %d", resp.StatusCode)
	}
	// original + 1 retry = 2 total calls
	if calls.Load() != 2 {
		t.Errorf("expected 2 API calls, got %d", calls.Load())
	}
}

func TestRetryOn401Transport_InjectsToken(t *testing.T) {
	var gotAuth string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer api.Close()

	tokenSrv := newFakeTokenServer(t)
	defer tokenSrv.Close()

	client := newTestOAuth2Client(t, tokenSrv)
	resp, err := client.Get(api.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()

	if gotAuth != "Bearer test-access-token" {
		t.Errorf("Authorization header: got %q, want %q", gotAuth, "Bearer test-access-token")
	}
}
