package updater

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"testing"
)

type sigAsset = struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// The embedded key is authoritative: KUSO_RELEASE_PUBLIC_KEY (a Secret
// value) must not be able to swap in an attacker's key on its own.
func TestReleaseKeyEnvCannotReplaceEmbedded(t *testing.T) {
	embPub, embPriv, _ := ed25519.GenerateKey(rand.Reader)
	envPub, envPriv, _ := ed25519.GenerateKey(rand.Reader)
	prev := embeddedReleaseKey
	embeddedReleaseKey = []byte(base64.StdEncoding.EncodeToString(embPub))
	t.Cleanup(func() { embeddedReleaseKey = prev })
	t.Setenv("KUSO_RELEASE_PUBLIC_KEY", base64.StdEncoding.EncodeToString(envPub))

	body := []byte(`{"version":"v1.0.0"}`)
	verify := func(priv ed25519.PrivateKey) error {
		sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, body))
		s := &Service{client: &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
			return jsonResp(200, sig), nil
		})}}
		return s.verifyManifestSignature(context.Background(), []sigAsset{{Name: "release.json.sig", BrowserDownloadURL: "https://example.test/sig"}}, body)
	}

	if err := verify(embPriv); err != nil {
		t.Fatalf("embedded-key signature rejected: %v", err)
	}
	if err := verify(envPriv); err == nil {
		t.Fatal("a signature under KUSO_RELEASE_PUBLIC_KEY replaced the embedded key")
	}
	t.Setenv("KUSO_RELEASE_PUBLIC_KEY_ROTATION", "true")
	if err := verify(envPriv); err != nil {
		t.Fatalf("explicit rotation key rejected: %v", err)
	}
	if err := verify(embPriv); err != nil {
		t.Fatalf("embedded key must stay trusted alongside a rotation key: %v", err)
	}
}
