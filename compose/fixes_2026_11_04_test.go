package compose

import (
	"testing"
)

func TestDedupeSlugs_NeverHandsOutSameNameTwice(t *testing.T) {
	got := dedupeSlugs([]string{"Worker", "worker", "worker-2"})
	seen := map[string]string{}
	for in, slug := range got {
		if prev, dup := seen[slug]; dup {
			t.Fatalf("%q and %q both got slug %q (%v)", prev, in, slug, got)
		}
		seen[slug] = in
	}
	if len(got) != 3 {
		t.Fatalf("want 3 slugs, got %v", got)
	}
}
