package kusoCli

import (
	"strings"
	"testing"

	"kuso/pkg/kusoApi"
)

// Execute() ALWAYS assigns the package-level api client — even when no
// instance is configured — so per-command `if api == nil` guards never
// fire on a fresh install. The friendly error must instead come from the
// client itself (central gate in pkg/kusoApi). Representative command:
// `kuso get projects`.
func TestUnconfiguredInstance_CommandReturnsFriendlyError(t *testing.T) {
	orig := api
	t.Cleanup(func() { api = orig })

	api = &kusoApi.KusoClient{}
	api.Init("", "") // what Execute() does when ~/.kuso has no instance

	err := getProjectsCmd.RunE(getProjectsCmd, nil)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "not configured") || !strings.Contains(msg, "kuso login") {
		t.Errorf("want a friendly not-configured error pointing at `kuso login`, got: %v", err)
	}
	if strings.Contains(msg, "unsupported protocol scheme") {
		t.Errorf("raw Go transport artifact leaked through: %v", err)
	}
}
