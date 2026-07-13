package kusoCli

import (
	"strings"
	"testing"

	"github.com/sislelabs/kuso/compose"
)

// TestComposeApplyGates covers the --apply safety gates: datastore
// conversions (fresh EMPTY addons — no data migrated) and unread
// env_file values both refuse to apply unless explicitly overridden.
func TestComposeApplyGates(t *testing.T) {
	docWithAddon := &compose.Doc{Addons: []compose.Addon{{Name: "db", Kind: "postgres"}}}
	docPlain := &compose.Doc{}
	repWithEnv := &compose.Report{UnresolvedEnvFiles: []string{".env.production"}}
	repPlain := &compose.Report{}

	reset := func() { importAllowEmptyAddons, importAllowMissingEnv = false, false }
	t.Cleanup(reset)

	// Clean conversion: nothing to gate.
	reset()
	if err := composeApplyGates(docPlain, repPlain); err != nil {
		t.Errorf("clean conversion should pass the gates, got: %v", err)
	}

	// Datastore conversion blocks without --allow-empty-addons.
	reset()
	err := composeApplyGates(docWithAddon, repPlain)
	if err == nil {
		t.Fatal("datastore conversion should refuse --apply without --allow-empty-addons")
	}
	if !strings.Contains(err.Error(), "db (postgres)") || !strings.Contains(err.Error(), "--allow-empty-addons") {
		t.Errorf("addon gate error should name the addon and the override flag, got: %v", err)
	}

	// --allow-empty-addons lets the addon conversion through.
	reset()
	importAllowEmptyAddons = true
	if err := composeApplyGates(docWithAddon, repPlain); err != nil {
		t.Errorf("--allow-empty-addons should pass the addon gate, got: %v", err)
	}

	// Unread env_file blocks without --allow-missing-env-files.
	reset()
	err = composeApplyGates(docPlain, repWithEnv)
	if err == nil {
		t.Fatal("unread env_file should refuse --apply without --allow-missing-env-files")
	}
	if !strings.Contains(err.Error(), ".env.production") || !strings.Contains(err.Error(), "--allow-missing-env-files") {
		t.Errorf("env-file gate error should list the file and the override flag, got: %v", err)
	}

	// --allow-missing-env-files lets it through.
	reset()
	importAllowMissingEnv = true
	if err := composeApplyGates(docPlain, repWithEnv); err != nil {
		t.Errorf("--allow-missing-env-files should pass the env-file gate, got: %v", err)
	}

	// Both risks present: both overrides required.
	reset()
	importAllowEmptyAddons = true
	if err := composeApplyGates(docWithAddon, repWithEnv); err == nil {
		t.Error("env-file gate should still block when only the addon gate is overridden")
	}
	importAllowMissingEnv = true
	if err := composeApplyGates(docWithAddon, repWithEnv); err != nil {
		t.Errorf("both overrides set should pass, got: %v", err)
	}
}
