package kusoCli

import (
	"strings"
	"testing"
)

func TestOutputContract(t *testing.T) {
	t.Cleanup(func() { outputFormat = "table"; outputFormatJSONOnly = "json" })
	// instance-config get -o table printed JSON and exited 0.
	_, err := runRoot(t, "instance-config", "get", "-o", "table")
	if err == nil || !strings.Contains(err.Error(), "only prints JSON") {
		t.Errorf("json-only view must refuse -o table, got %v", err)
	}
	_, err = runRoot(t, "instance-config", "banner", "-o", "table")
	if err == nil || !strings.Contains(err.Error(), "only prints JSON") {
		t.Errorf("banner must refuse -o table, got %v", err)
	}
	// env set -o json printed human text.
	_, err = runRoot(t, "env", "set", "p", "s", "K=V", "-o", "json")
	if err == nil || !strings.Contains(err.Error(), "no machine-readable output") {
		t.Errorf("text-only mutator must refuse -o json, got %v", err)
	}
}
