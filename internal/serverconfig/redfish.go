package serverconfig

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/hashicorp/go-retryablehttp"
)

// redfishClient is a thin HTTPS client targeting one iDRAC. Holds basic-auth
// credentials and a TLS-skip-verify transport (iDRACs ship with self-signed
// certs in the prototype; cert pinning is post-MVP). The HTTP client is
// retryablehttp-wrapped so transient 5xx / connection errors get retried with
// hashicorp's default exponential backoff (no jitter — single-iDRAC traffic
// won't benefit from jitter's thundering-herd avoidance).
type redfishClient struct {
	host     string
	username string
	password string
	http     *http.Client
}

// Retry tuning. iDRACs occasionally return 503 under load or close idle
// connections; a small fixed retry budget with jittered exponential backoff
// smooths those without hiding real failures.
const (
	retryMax     = 3
	retryWaitMin = 500 * time.Millisecond
	retryWaitMax = 5 * time.Second
	httpTimeout  = 10 * time.Second
)

func newRedfishClient(host, user, pass string) *redfishClient {
	rc := retryablehttp.NewClient()
	rc.RetryMax = retryMax
	rc.RetryWaitMin = retryWaitMin
	rc.RetryWaitMax = retryWaitMax
	// Backoff defaults to retryablehttp.DefaultBackoff: exponential
	// (min * 2^attempt, capped at max) with Retry-After honored on 429/503.
	// CheckRetry defaults to retryablehttp.DefaultRetryPolicy: retry on 5xx
	// + connection errors, do NOT retry 4xx — exactly what we want.
	rc.Logger = nil // silence the default stderr logger; reconcile logs cover what we need
	rc.HTTPClient.Transport = &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	rc.HTTPClient.Timeout = httpTimeout
	return &redfishClient{
		host:     host,
		username: user,
		password: pass,
		http:     rc.StandardClient(),
	}
}

// dellAttributesURL is the canonical, discoverable iDRAC 9 attributes resource.
// Backs every Dell-specific toggle the controller cares about (SSH, RACADM,
// IPMI, etc.) — verified empirically to be the same backing resource as the
// non-discoverable /Attributes alias.
func (c *redfishClient) dellAttributesURL() string {
	return fmt.Sprintf("https://%s/redfish/v1/Managers/iDRAC.Embedded.1/Oem/Dell/DellAttributes/iDRAC.Embedded.1", c.host)
}

// attributesResponse is the partial view of the DellAttributes resource we
// consume. The real payload has ~1200 keys; we don't model the rest.
type attributesResponse struct {
	Attributes map[string]any `json:"Attributes"`
}

// GetAttributes returns the iDRAC attribute map. Values are mixed types
// (mostly string, some int) — callers index by key and assert as needed.
func (c *redfishClient) GetAttributes(ctx context.Context) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.dellAttributesURL(), nil)
	if err != nil {
		return nil, fmt.Errorf("build GET: %w", err)
	}
	req.SetBasicAuth(c.username, c.password)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET Attributes: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GET Attributes: HTTP %d: %s", resp.StatusCode, string(body))
	}

	var ar attributesResponse
	if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil {
		return nil, fmt.Errorf("decode Attributes: %w", err)
	}
	return ar.Attributes, nil
}

// PatchAttributes sends a single PATCH carrying the given key→value pairs.
// iDRAC PATCHes are idempotent — sending the current value returns 200 without
// changing anything. Empty map is a no-op (no request).
func (c *redfishClient) PatchAttributes(ctx context.Context, updates map[string]string) error {
	if len(updates) == 0 {
		return nil
	}
	body, err := json.Marshal(map[string]any{"Attributes": updates})
	if err != nil {
		return fmt.Errorf("marshal PATCH body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, c.dellAttributesURL(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build PATCH: %w", err)
	}
	req.SetBasicAuth(c.username, c.password)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("PATCH Attributes: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("PATCH Attributes: HTTP %d", resp.StatusCode)
	}
	return nil
}
