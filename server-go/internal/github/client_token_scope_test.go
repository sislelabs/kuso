package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testClient builds a *Client with a freshly-generated RSA key (so the
// App-JWT transport can sign) whose App API calls are redirected to srv.
func testClient(t *testing.T, srvURL string) *Client {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	c, err := NewClient(&Config{AppID: 12345, PrivateKey: pemBytes})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c == nil {
		t.Fatal("NewClient returned nil (config not recognised as configured)")
	}
	c.baseURL = srvURL
	return c
}

// TestMintRepoScopedToken_CarriesRepoRestriction is the HIGH-3 regression:
// the token minted for the build clone path MUST be restricted to the
// single repo being built (repository_ids/repositories), not the whole
// installation.
func TestMintRepoScopedToken_CarriesRepoRestriction(t *testing.T) {
	var gotBody map[string]any
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"ghs_scopedtoken","expires_at":"2099-01-01T00:00:00Z"}`))
	}))
	defer srv.Close()

	c := testClient(t, srv.URL)
	tok, err := c.MintRepoScopedToken(context.Background(), 999, "example", "web")
	if err != nil {
		t.Fatalf("MintRepoScopedToken: %v", err)
	}
	if tok != "ghs_scopedtoken" {
		t.Errorf("token: %q", tok)
	}
	// Must hit the installation access_tokens endpoint for installation 999.
	if !strings.Contains(gotPath, "/app/installations/999/access_tokens") {
		t.Errorf("unexpected path: %q", gotPath)
	}
	// Must carry the single-repo restriction.
	repos, ok := gotBody["repositories"].([]any)
	if !ok || len(repos) != 1 || repos[0] != "web" {
		t.Fatalf("token request missing single-repo restriction; body=%v", gotBody)
	}
	perms, _ := gotBody["permissions"].(map[string]any)
	if len(perms) != 1 || perms["contents"] != "read" {
		t.Fatalf("token request must ask for contents:read only; permissions=%v", gotBody["permissions"])
	}
}

// With no repo to scope by, the mint must fail rather than fall back to an
// installation-wide token (which would go to api.github.com, not srv).
func TestMintRepoScopedToken_EmptyRepoRefusesInstallationWide(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("empty repo reached the token endpoint: %s", r.URL.Path)
	}))
	defer srv.Close()

	c := testClient(t, srv.URL)
	_, err := c.MintRepoScopedToken(context.Background(), 1, "", "")
	if err == nil || !strings.Contains(err.Error(), "needs owner and repo") {
		t.Fatalf("empty repo must be refused before any mint, got err=%v", err)
	}
}
