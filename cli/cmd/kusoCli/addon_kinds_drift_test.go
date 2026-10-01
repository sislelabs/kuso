package kusoCli

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The CLI's kind lists are hand-copied from server-go; this reads the
// server source so the next drift (mysql was "reserved" in the CLI but
// deployable on the server) fails here instead of shipping.
func TestAddonKindsMatchServer(t *testing.T) {
	src, err := os.ReadFile("../../../server-go/internal/addons/addons.go")
	if err != nil {
		t.Skipf("server source not available: %v", err)
	}
	extract := func(re string) []string {
		m := regexp.MustCompile(re).FindSubmatch(src)
		if m == nil {
			t.Fatalf("pattern %q not found in addons.go", re)
		}
		var out []string
		for _, q := range regexp.MustCompile(`"([a-z0-9]+)"`).FindAllSubmatch(m[1], -1) {
			out = append(out, string(q[1]))
		}
		sort.Strings(out)
		return out
	}
	sorted := func(in []string) []string {
		out := append([]string(nil), in...)
		sort.Strings(out)
		return out
	}
	if got, want := sorted(supportedAddonKinds), extract(`(?s)var SupportedKinds = \[\]string\{(.*?)\}`); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("supportedAddonKinds = %v, server SupportedKinds = %v", got, want)
	}
	if got, want := sorted(reservedAddonKinds), extract(`var reservedKinds = \[\]string\{(.*?)\}`); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("reservedAddonKinds = %v, server reservedKinds = %v", got, want)
	}
}
