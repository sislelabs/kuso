package config

import (
	"strings"
	"testing"
)

func TestFromEnv(t *testing.T) {
	t.Run("trims trailing slash from URL", func(t *testing.T) {
		t.Setenv("KUSO_URL", "https://kuso.example.com/")
		t.Setenv("KUSO_TOKEN", "tok")
		cfg, err := FromEnv()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.URL != "https://kuso.example.com" {
			t.Errorf("URL = %q, want %q", cfg.URL, "https://kuso.example.com")
		}
	})

	t.Run("missing URL is an error", func(t *testing.T) {
		t.Setenv("KUSO_URL", "")
		t.Setenv("KUSO_TOKEN", "tok")
		_, err := FromEnv()
		if err == nil || !strings.Contains(err.Error(), "KUSO_URL") {
			t.Fatalf("expected KUSO_URL error, got %v", err)
		}
	})

	t.Run("missing token is an error", func(t *testing.T) {
		t.Setenv("KUSO_URL", "https://kuso.example.com")
		t.Setenv("KUSO_TOKEN", "")
		_, err := FromEnv()
		if err == nil || !strings.Contains(err.Error(), "KUSO_TOKEN") {
			t.Fatalf("expected KUSO_TOKEN error, got %v", err)
		}
	})
}
