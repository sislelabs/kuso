package coolify

import "testing"

func TestAssignKusoSlugs_NeverHandsOutSameSlugTwice(t *testing.T) {
	names := []string{"acme", "acme-2", "Acme!", "ACME"}
	out := AssignKusoSlugs(names)
	seen := map[string]string{}
	for _, n := range names {
		s := out[n]
		if prev, dup := seen[s]; dup {
			t.Fatalf("%q and %q both got slug %q (all: %v)", prev, n, s, out)
		}
		seen[s] = n
	}
	if out["acme"] != "acme" || out["acme-2"] != "acme-2" {
		t.Errorf("first-come names should keep their bare slug: %v", out)
	}
}
