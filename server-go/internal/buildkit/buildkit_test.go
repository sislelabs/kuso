package buildkit

import "testing"

func TestDefaultBuildCap(t *testing.T) {
	cases := []struct {
		name        string
		milli       int64
		daemons, mp int
		want        int
	}{
		{"2-core box floors at 2", 2000, 1, 3, 2},
		{"16 cores, one daemon", 16000, 1, 3, 4},
		{"20 x 8 cores, one daemon: daemon-bound", 160000, 1, 3, 6},
		{"20 x 8 cores, three shards", 160000, 3, 3, 18},
		{"unknown CPU uses daemon capacity", 0, 1, 3, 6},
		{"zero daemons treated as one", 160000, 0, 0, 6},
	}
	for _, tc := range cases {
		if got := DefaultBuildCap(tc.milli, tc.daemons, tc.mp); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestHostFor(t *testing.T) {
	if got := hostFor("acme", 0); got != DefaultHost {
		t.Fatalf("unsharded: got %q", got)
	}
	a, b := hostFor("acme", 3), hostFor("acme", 3)
	if a != b || a == DefaultHost {
		t.Fatalf("sharded host must be stable and not the default: %q %q", a, b)
	}
	seen := map[string]bool{}
	for _, p := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		seen[hostFor(p, 3)] = true
	}
	if len(seen) < 2 {
		t.Fatalf("8 projects all hashed to one shard: %v", seen)
	}
}
