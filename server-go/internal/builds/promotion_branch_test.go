package builds

import "testing"

// TestPromotionBranchMatches is the MED-6 guard: an env with an unset
// branch must be treated as the default-branch env, so a push to a
// feature/staging branch does NOT promote to it.
func TestPromotionBranchMatches(t *testing.T) {
	t.Parallel()
	cases := []struct {
		build, env, def string
		want            bool
	}{
		{"main", "main", "main", true},           // exact
		{"staging", "staging", "main", true},      // exact non-default
		{"staging", "main", "main", false},        // no cross-branch
		{"staging", "", "main", false},            // unset env ≠ staging build (THE bug)
		{"main", "", "main", true},                // unset env = default build
		{"feature/x", "", "main", false},          // feature never hits unset-branch env
		{"", "production", "main", true},          // manual no-branch trigger matches any
		{"", "", "main", true},                    // both unset
		{"develop", "", "develop", true},          // custom default branch
	}
	for _, c := range cases {
		if got := promotionBranchMatches(c.build, c.env, c.def); got != c.want {
			t.Errorf("promotionBranchMatches(%q,%q,%q)=%v want %v", c.build, c.env, c.def, got, c.want)
		}
	}
}
