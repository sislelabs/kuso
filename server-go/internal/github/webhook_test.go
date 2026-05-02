package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func sign(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifySignature(t *testing.T) {
	t.Parallel()
	body := `{"hello":"world"}`
	const secret = "shh"
	good := sign(secret, body)

	if !VerifySignature(secret, []byte(body), good) {
		t.Error("good signature rejected")
	}
	if VerifySignature("wrong", []byte(body), good) {
		t.Error("wrong secret accepted")
	}
	if VerifySignature(secret, []byte(body+"x"), good) {
		t.Error("tampered body accepted")
	}
	if VerifySignature(secret, []byte(body), strings.Replace(good, "sha256=", "sha1=", 1)) {
		t.Error("wrong prefix accepted")
	}
	if VerifySignature(secret, []byte(body), "") {
		t.Error("empty signature accepted")
	}
	if VerifySignature("", []byte(body), good) {
		t.Error("empty secret accepted")
	}
	if VerifySignature(secret, []byte(body), "sha256=zzz") {
		t.Error("malformed hex accepted")
	}
}

func TestRepoMatches(t *testing.T) {
	t.Parallel()
	cases := []struct {
		url      string
		fullName string
		want     bool
	}{
		{"https://github.com/example/alpha", "example/alpha", true},
		{"https://github.com/example/alpha.git", "example/alpha", true},
		{"https://GITHUB.com/Example/Alpha", "example/alpha", true},
		{"https://github.com/example/alpha", "example/beta", false},
		{"", "example/alpha", false},
		// Embedded match without "/" boundary should NOT match (substring guard).
		{"https://github.com/owner/yalpha", "alpha", false},
	}
	for _, tc := range cases {
		if got := repoMatches(tc.url, tc.fullName); got != tc.want {
			t.Errorf("repoMatches(%q, %q): got %v, want %v", tc.url, tc.fullName, got, tc.want)
		}
	}
}

func TestLoadConfig(t *testing.T) {
	// t.Setenv must not be combined with t.Parallel — they're
	// incompatible by design.
	t.Run("disabled", func(t *testing.T) {
		t.Setenv("GITHUB_APP_ID", "")
		t.Setenv("GITHUB_APP_PRIVATE_KEY", "")
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatal(err)
		}
		if cfg != nil {
			t.Errorf("expected nil cfg when fully unset, got %+v", cfg)
		}
	})
	t.Run("partial errors", func(t *testing.T) {
		t.Setenv("GITHUB_APP_ID", "123")
		t.Setenv("GITHUB_APP_PRIVATE_KEY", "")
		if _, err := LoadConfig(); err == nil {
			t.Error("expected error for partial config")
		}
	})
	t.Run("good", func(t *testing.T) {
		t.Setenv("GITHUB_APP_ID", "12345")
		t.Setenv("GITHUB_APP_PRIVATE_KEY", `-----BEGIN RSA PRIVATE KEY-----\nLine\n-----END RSA PRIVATE KEY-----`)
		t.Setenv("GITHUB_APP_WEBHOOK_SECRET", "sec")
		t.Setenv("GITHUB_APP_SLUG", "my-app")
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.AppID != 12345 {
			t.Errorf("AppID: %d", cfg.AppID)
		}
		// Literal "\n" must be unfolded to actual newlines so the PEM
		// parses downstream.
		if !strings.Contains(string(cfg.PrivateKey), "\n") || strings.Contains(string(cfg.PrivateKey), `\n`) {
			t.Errorf("PrivateKey not unfolded: %q", string(cfg.PrivateKey))
		}
		if cfg.InstallURL() != "https://github.com/apps/my-app/installations/new" {
			t.Errorf("InstallURL: %q", cfg.InstallURL())
		}
		if !cfg.IsConfigured() {
			t.Error("IsConfigured false")
		}
	})
}
