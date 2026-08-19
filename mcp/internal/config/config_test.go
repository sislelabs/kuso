package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolateHome points $HOME at a fresh temp dir so tests never see the
// developer's real ~/.kuso, and returns its path for seeding files.
func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func writeKusoFile(t *testing.T, home, name, content string) {
	t.Helper()
	dir := filepath.Join(home, ".kuso")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFromEnv(t *testing.T) {
	t.Run("env vars win when both set", func(t *testing.T) {
		home := isolateHome(t)
		// A conflicting CLI config must be ignored entirely.
		writeKusoFile(t, home, "kuso.yaml", "instances:\n  other.example.com:\n    apiurl: https://other.example.com\ncurrentInstance: other.example.com\n")
		writeKusoFile(t, home, "credentials.yaml", "other.example.com: filetok\n")
		t.Setenv("KUSO_URL", "https://kuso.example.com/")
		t.Setenv("KUSO_TOKEN", "envtok")
		cfg, err := FromEnv()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.URL != "https://kuso.example.com" {
			t.Errorf("URL = %q, want %q (trailing slash trimmed)", cfg.URL, "https://kuso.example.com")
		}
		if cfg.Token != "envtok" {
			t.Errorf("Token = %q, want env token", cfg.Token)
		}
	})

	t.Run("missing URL with no CLI config is an error", func(t *testing.T) {
		isolateHome(t)
		t.Setenv("KUSO_URL", "")
		t.Setenv("KUSO_TOKEN", "tok")
		_, err := FromEnv()
		if err == nil || !strings.Contains(err.Error(), "KUSO_URL") {
			t.Fatalf("expected KUSO_URL error, got %v", err)
		}
	})

	t.Run("missing token with no CLI config is an error", func(t *testing.T) {
		isolateHome(t)
		t.Setenv("KUSO_URL", "https://kuso.example.com")
		t.Setenv("KUSO_TOKEN", "")
		_, err := FromEnv()
		if err == nil || !strings.Contains(err.Error(), "KUSO_TOKEN") {
			t.Fatalf("expected KUSO_TOKEN error, got %v", err)
		}
	})
}

func TestFromEnvCredentialsFallback(t *testing.T) {
	t.Setenv("KUSO_URL", "")
	t.Setenv("KUSO_TOKEN", "")

	t.Run("current instance supplies URL and token", func(t *testing.T) {
		home := isolateHome(t)
		writeKusoFile(t, home, "kuso.yaml", `instances:
  kuso.a.com:
    apiurl: https://kuso.a.com
  kuso.b.com:
    apiurl: https://kuso.b.com/
currentInstance: kuso.b.com
`)
		writeKusoFile(t, home, "credentials.yaml", "kuso.a.com: tok-a\nkuso.b.com: tok-b\n")
		cfg, err := FromEnv()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.URL != "https://kuso.b.com" {
			t.Errorf("URL = %q, want current instance apiurl (trimmed)", cfg.URL)
		}
		if cfg.Token != "tok-b" {
			t.Errorf("Token = %q, want tok-b", cfg.Token)
		}
	})

	t.Run("sole instance is used without currentInstance", func(t *testing.T) {
		home := isolateHome(t)
		writeKusoFile(t, home, "kuso.yaml", "instances:\n  kuso.solo.com:\n    apiurl: https://kuso.solo.com\n")
		writeKusoFile(t, home, "credentials.yaml", "kuso.solo.com: solotok\n")
		cfg, err := FromEnv()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.URL != "https://kuso.solo.com" || cfg.Token != "solotok" {
			t.Errorf("got %q/%q, want sole instance", cfg.URL, cfg.Token)
		}
	})

	t.Run("multiple instances without currentInstance is an error", func(t *testing.T) {
		home := isolateHome(t)
		writeKusoFile(t, home, "kuso.yaml", `instances:
  kuso.a.com:
    apiurl: https://kuso.a.com
  kuso.b.com:
    apiurl: https://kuso.b.com
`)
		writeKusoFile(t, home, "credentials.yaml", "kuso.a.com: tok-a\n")
		_, err := FromEnv()
		if err == nil || !strings.Contains(err.Error(), "multiple instances") {
			t.Fatalf("expected multiple-instances error, got %v", err)
		}
		// Both choices are named so the user can pick.
		if !strings.Contains(err.Error(), "kuso.a.com") || !strings.Contains(err.Error(), "kuso.b.com") {
			t.Errorf("error should name the instances: %v", err)
		}
	})

	t.Run("env URL + file token, matched by apiurl", func(t *testing.T) {
		home := isolateHome(t)
		writeKusoFile(t, home, "kuso.yaml", `instances:
  prod:
    apiurl: https://kuso.prod.com
  stage:
    apiurl: https://kuso.stage.com
currentInstance: prod
`)
		writeKusoFile(t, home, "credentials.yaml", "prod: prodtok\nstage: stagetok\n")
		t.Setenv("KUSO_URL", "https://kuso.stage.com/")
		cfg, err := FromEnv()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Explicit KUSO_URL overrides currentInstance for token selection.
		if cfg.URL != "https://kuso.stage.com" || cfg.Token != "stagetok" {
			t.Errorf("got %q/%q, want stage instance token", cfg.URL, cfg.Token)
		}
	})

	t.Run("env URL + file token, matched by URL host when kuso.yaml is absent", func(t *testing.T) {
		home := isolateHome(t)
		writeKusoFile(t, home, "credentials.yaml", "kuso.example.com: hosttok\n")
		t.Setenv("KUSO_URL", "https://kuso.example.com")
		cfg, err := FromEnv()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Token != "hosttok" {
			t.Errorf("Token = %q, want host-keyed credential", cfg.Token)
		}
	})

	t.Run("env token + file URL", func(t *testing.T) {
		home := isolateHome(t)
		writeKusoFile(t, home, "kuso.yaml", "instances:\n  kuso.x.com:\n    apiurl: https://kuso.x.com\ncurrentInstance: kuso.x.com\n")
		t.Setenv("KUSO_TOKEN", "envtok")
		cfg, err := FromEnv()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.URL != "https://kuso.x.com" || cfg.Token != "envtok" {
			t.Errorf("got %q/%q, want file URL + env token", cfg.URL, cfg.Token)
		}
	})

	t.Run("lowercased credential keys still match (viper writer)", func(t *testing.T) {
		home := isolateHome(t)
		writeKusoFile(t, home, "kuso.yaml", "instances:\n  Kuso.Mixed.Com:\n    apiurl: https://kuso.mixed.com\ncurrentInstance: Kuso.Mixed.Com\n")
		writeKusoFile(t, home, "credentials.yaml", "kuso.mixed.com: mixedtok\n")
		cfg, err := FromEnv()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Token != "mixedtok" {
			t.Errorf("Token = %q, want case-insensitive match", cfg.Token)
		}
	})

	t.Run("instance without a credential is a KUSO_TOKEN error", func(t *testing.T) {
		home := isolateHome(t)
		writeKusoFile(t, home, "kuso.yaml", "instances:\n  kuso.nocred.com:\n    apiurl: https://kuso.nocred.com\ncurrentInstance: kuso.nocred.com\n")
		_, err := FromEnv()
		if err == nil || !strings.Contains(err.Error(), "KUSO_TOKEN") {
			t.Fatalf("expected KUSO_TOKEN error, got %v", err)
		}
	})

	t.Run("malformed files degrade to the env-var errors", func(t *testing.T) {
		home := isolateHome(t)
		writeKusoFile(t, home, "kuso.yaml", ":: not yaml ::[")
		writeKusoFile(t, home, "credentials.yaml", "also: [broken")
		_, err := FromEnv()
		if err == nil || !strings.Contains(err.Error(), "KUSO_URL") {
			t.Fatalf("expected KUSO_URL error, got %v", err)
		}
	})
}
