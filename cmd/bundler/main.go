package main

import (
	"log"
	"net/http"

	"github.com/armada/configbundle/internal/bundler"
	"github.com/armada/configbundle/internal/version"
)

func main() {
	cfg, err := bundler.NewConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	httpClient := buildHTTPClient(cfg)

	orbital := &bundler.HTTPOrbitalClient{
		URL:        cfg.OrbitalGraphQLURL,
		APIURL:     cfg.OrbitalAPIURL,
		HTTPClient: httpClient,
	}

	mux := http.NewServeMux()
	mux.Handle("POST /bundle", &bundler.Handler{Orbital: orbital, Resolutions: orbital})

	log.Printf("bundler starting version=%s port=%s orbital=%s", version.Version, cfg.Port, cfg.OrbitalGraphQLURL)
	if err := http.ListenAndServe(":"+cfg.Port, mux); err != nil {
		log.Fatalf("bundler: %v", err)
	}
}

// buildHTTPClient returns an auth-aware http.Client based on config priority:
//  1. ORBITAL_BEARER_TOKEN — static bearer (deprecated fallback)
//  2. ORBITAL_CLIENT_ID set — OAuth2 client credentials (Azure AD)
//  3. Neither — plain client (for local dev without auth)
func buildHTTPClient(cfg *bundler.Config) *http.Client {
	if cfg.OrbitalBearerToken != "" {
		log.Println("bundler: using static bearer token for Orbital auth (ORBITAL_BEARER_TOKEN)")
		return bundler.NewStaticBearerHTTPClient(cfg.OrbitalBearerToken)
	}
	if cfg.OIDCClientSecret != "" {
		log.Printf("bundler: using OAuth2 client credentials for Orbital auth (clientId: %s)", cfg.OIDCClientID)
		return bundler.NewOAuth2HTTPClient(cfg)
	}
	log.Println("bundler: ORBITAL_OIDC_CLIENT_SECRET not set — using plain HTTP (requests to Orbital will 401)")
	return &http.Client{Timeout: bundler.DefaultHTTPTimeout}
}
