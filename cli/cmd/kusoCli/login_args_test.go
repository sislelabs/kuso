package kusoCli

import (
	"strings"
	"testing"

	"kuso/pkg/kusoApi"
)

// The first error a new user sees told them to run
// `kuso login <instance-url>`, which login rejected as an unknown command.
func TestLoginAcceptsPositionalURL(t *testing.T) {
	if err := loginCmd.Args(loginCmd, []string{"https://kuso.example.com"}); err != nil {
		t.Fatalf("login must accept a positional instance URL: %v", err)
	}
	if !strings.Contains(kusoApi.ErrNotConfigured.Error(), "kuso login --api") {
		t.Errorf("not-configured hint should name a working login form: %v", kusoApi.ErrNotConfigured)
	}
	t.Cleanup(func() { loginAPIURL = "" })
	err := loginCmd.RunE(loginCmd, []string{"admin"})
	if err == nil || !strings.Contains(err.Error(), "not an instance URL") {
		t.Errorf("a non-URL positional must be refused, got %v", err)
	}
}

// doctor read KUSO_SERVER while every other command read KUSO_API_URL,
// so it vouched for a server the CLI wasn't using.
func TestResolveAPIURL_EnvPrecedence(t *testing.T) {
	orig := currentInstance
	t.Cleanup(func() { currentInstance = orig })
	currentInstance.ApiUrl = "https://saved.example.com"

	t.Setenv("KUSO_API_URL", "")
	t.Setenv("KUSO_URL", "")
	if got := resolveAPIURL(); got != "https://saved.example.com" {
		t.Errorf("saved instance: got %q", got)
	}
	t.Setenv("KUSO_URL", "https://mcp.example.com")
	if got := resolveAPIURL(); got != "https://mcp.example.com" {
		t.Errorf("KUSO_URL: got %q", got)
	}
	t.Setenv("KUSO_API_URL", "https://api.example.com")
	if got := resolveAPIURL(); got != "https://api.example.com" {
		t.Errorf("KUSO_API_URL must win: got %q", got)
	}
}
