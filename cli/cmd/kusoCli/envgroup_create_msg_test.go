package kusoCli

import (
	"strings"
	"testing"
)

// #7: the server now reports which literals it retargeted from a sibling's
// production URL to its clone, and which project-domain URLs it couldn't
// map. `env-group create` must show both, warnings flagged loudly.
func TestEnvGroupCreateMsg_ShowsRewritesAndWarnings(t *testing.T) {
	body := []byte(`{"name":"qa","rewrittenEnvVars":["web-qa: API_BASE"],"warnings":["web-qa: CDN still points at cdn.e2e.example.com, ..."]}`)
	got, err := envGroupCreateMsg("e2e", "qa", body)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"env-group e2e/qa created",
		"retargeted at this group's clones:\n  web-qa: API_BASE",
		"WARNING",
		"  web-qa: CDN still points at cdn.e2e.example.com",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("message missing %q:\n%s", want, got)
		}
	}

	plain, err := envGroupCreateMsg("e2e", "qa", []byte(`{"name":"qa"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain, "WARNING") || strings.Contains(plain, "retargeted") {
		t.Errorf("nothing rewritten/warned, yet: %q", plain)
	}
}
