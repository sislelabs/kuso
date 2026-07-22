package updater

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// rtFunc adapts a func to an http.RoundTripper.
type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewReader([]byte(body))),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

// TestFetchVersion_FallsBackEmptyOperatorImage is the regression guard for
// the operator-downgrade hardening: a release.json that omits (or blanks)
// the operator/server image must NOT roll the Deployment to an empty image.
// fetchVersion fills it from the version-tagged default.
func TestFetchVersion_FallsBackEmptyOperatorImage(t *testing.T) {
	t.Setenv("KUSO_REQUIRE_SIGNATURES", "false")

	const manifestURL = "https://example.test/release.json"
	// A manifest with a server image but NO operator image (the dangerous
	// shape — a blank operator.image would blank the operator Deployment).
	manifest := `{"version":"v9.9.9","components":{"server":{"image":"ghcr.io/sislelabs/kuso-server-go:v9.9.9"},"operator":{"image":""}}}`
	ghRelease := `{"tag_name":"v9.9.9","body":"notes","assets":[{"name":"release.json","browser_download_url":"` + manifestURL + `"}]}`

	s := &Service{
		Repo:    "sislelabs/kuso",
		Current: "v9.9.8",
		client: &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			switch {
			case strings.Contains(r.URL.Host, "api.github.com"):
				return jsonResp(200, ghRelease), nil
			case r.URL.String() == manifestURL:
				return jsonResp(200, manifest), nil
			default:
				return jsonResp(404, `{}`), nil
			}
		})},
	}

	m, err := s.fetchVersion(context.Background(), "v9.9.9")
	if err != nil {
		t.Fatalf("fetchVersion: %v", err)
	}
	if m.Components.Operator.Image != "ghcr.io/sislelabs/kuso-operator:v9.9.9" {
		t.Errorf("operator image should fall back to the version-tagged default, got %q", m.Components.Operator.Image)
	}
	if m.Components.Server.Image != "ghcr.io/sislelabs/kuso-server-go:v9.9.9" {
		t.Errorf("server image should be preserved from the manifest, got %q", m.Components.Server.Image)
	}
}

// TestFetchVersion_RefusesSynthWhenKeyConfigured is the supply-chain
// regression: a release with NO release.json (hence no signature to
// verify) must NOT be deployed when a signing public key is wired and
// signatures are required. Previously fetchVersion synthesized a
// manifest with hardcoded ghcr tags and returned BEFORE any signature
// gate — letting an attacker who can publish/spoof a GH release ship an
// unsigned upgrade. With a key present + require=on we now fail closed.
func TestFetchVersion_RefusesSynthWhenKeyConfigured(t *testing.T) {
	// A real 32-byte Ed25519 pubkey (base64). Wiring it via the env var
	// makes resolveReleasePubKey() non-empty regardless of the embedded
	// releasekey.pub state.
	t.Setenv("KUSO_RELEASE_PUBLIC_KEY", "PA/PGYRHPzk4HkFDek3poIQjFeOyMc+5Nq5zBLhnRks=")
	t.Setenv("KUSO_REQUIRE_SIGNATURES", "true")

	ghRelease := `{"tag_name":"v8.8.8","body":"n","assets":[]}`
	s := &Service{
		Repo:    "sislelabs/kuso",
		Current: "v8.8.7",
		client: &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			return jsonResp(200, ghRelease), nil
		})},
	}
	if _, err := s.fetchVersion(context.Background(), "v8.8.8"); err == nil {
		t.Fatal("fetchVersion synthesized an unsigned manifest with a signing key configured — want refusal")
	} else if !strings.Contains(err.Error(), "refusing to deploy an unsigned manifest") {
		t.Errorf("wrong error: %v", err)
	}
}

// TestFetchLatest_RefusesSynthWhenKeyConfigured is the fetchLatest
// (background ticker / non-pinned) counterpart to the above.
func TestFetchLatest_RefusesSynthWhenKeyConfigured(t *testing.T) {
	t.Setenv("KUSO_RELEASE_PUBLIC_KEY", "PA/PGYRHPzk4HkFDek3poIQjFeOyMc+5Nq5zBLhnRks=")
	t.Setenv("KUSO_REQUIRE_SIGNATURES", "true")

	ghRelease := `{"tag_name":"v8.8.8","body":"n","assets":[]}`
	s := &Service{
		Repo:    "sislelabs/kuso",
		Current: "v8.8.7",
		client: &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			return jsonResp(200, ghRelease), nil
		})},
	}
	if _, _, err := s.fetchLatest(context.Background()); err == nil {
		t.Fatal("fetchLatest synthesized an unsigned manifest with a signing key configured — want refusal")
	} else if !strings.Contains(err.Error(), "refusing to deploy an unsigned manifest") {
		t.Errorf("wrong error: %v", err)
	}
}

// TestFetchVersion_SynthesizesWhenNoManifest covers the no-release.json
// path: both images are synthesized from the tag.
func TestFetchVersion_SynthesizesWhenNoManifest(t *testing.T) {
	t.Setenv("KUSO_REQUIRE_SIGNATURES", "false")
	ghRelease := `{"tag_name":"v8.8.8","body":"n","assets":[]}`
	s := &Service{
		Repo:    "sislelabs/kuso",
		Current: "v8.8.7",
		client: &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			return jsonResp(200, ghRelease), nil
		})},
	}
	m, err := s.fetchVersion(context.Background(), "v8.8.8")
	if err != nil {
		t.Fatalf("fetchVersion: %v", err)
	}
	if m.Components.Operator.Image != "ghcr.io/sislelabs/kuso-operator:v8.8.8" {
		t.Errorf("operator image: %q", m.Components.Operator.Image)
	}
}
