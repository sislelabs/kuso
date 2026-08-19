package kusoApi

import (
	"errors"
	"strings"
	"testing"
)

// A fresh install has no saved instance, but the CLI still constructs
// and Init()s the client with an empty URL so `kuso login` works. Every
// request must then fail with the friendly ErrNotConfigured — not
// resty's raw `unsupported protocol scheme ""`.
func TestUnconfigured_RequestsReturnFriendlyError(t *testing.T) {
	k := &KusoClient{}
	k.Init("", "")

	_, err := k.GetProjects()
	if err == nil {
		t.Fatal("expected an error from an unconfigured client, got nil")
	}
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("errors.Is(err, ErrNotConfigured) = false; got: %v", err)
	}
	if !strings.Contains(err.Error(), "kuso login") {
		t.Errorf("error should point at `kuso login`, got: %v", err)
	}
	if strings.Contains(err.Error(), "unsupported protocol scheme") {
		t.Errorf("raw Go transport artifact leaked through: %v", err)
	}
}

// Same gate must cover the raw escape hatch (`kuso api …`).
func TestUnconfigured_RawGetReturnsFriendlyError(t *testing.T) {
	k := &KusoClient{}
	k.Init("", "")

	_, err := k.RawGet("/api/projects")
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("errors.Is(err, ErrNotConfigured) = false; got: %v", err)
	}
}

// A configured client must be unaffected by the gate (covered further
// by raw_test.go; this pins the middleware doesn't misfire).
func TestConfigured_GateDoesNotFire(t *testing.T) {
	k := &KusoClient{}
	k.Init("http://localhost:1", "tok") // connection refused is fine — just not ErrNotConfigured
	_, err := k.GetProjects()
	if errors.Is(err, ErrNotConfigured) {
		t.Fatalf("gate fired on a configured client: %v", err)
	}
}
