package pkgupdates

import "testing"

func TestParseAnnotation(t *testing.T) {
	t.Parallel()

	// Empty → not present, not an error.
	if a := ParseAnnotation("n1", ""); a.Present || a.HasUpdates() {
		t.Errorf("empty annotation should be not-present: %+v", a)
	}
	// Malformed JSON → not present (probe rewrites next tick).
	if a := ParseAnnotation("n1", "{not json"); a.Present {
		t.Errorf("malformed annotation should be not-present: %+v", a)
	}

	raw := `{"count":50,"rebootRequired":true,"pkgMgr":"apt",` +
		`"sample":["base-files 1->2","coreutils 9.4->9.5"],"checkedAt":"2026-06-01T18:30:32Z"}`
	a := ParseAnnotation("server2", raw)
	if !a.Present || a.Count != 50 || !a.RebootRequired || a.PkgMgr != "apt" {
		t.Errorf("parse: %+v", a)
	}
	if len(a.Sample) != 2 || a.CheckedAt != "2026-06-01T18:30:32Z" {
		t.Errorf("parse sample/checkedAt: %+v", a)
	}
	if !a.HasUpdates() {
		t.Error("50 apt updates should be HasUpdates")
	}
}

func TestHasUpdates(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		a    Advisory
		want bool
	}{
		{"not present", Advisory{Present: false, Count: 5, PkgMgr: "apt"}, false},
		{"zero count", Advisory{Present: true, Count: 0, PkgMgr: "apt"}, false},
		{"unsupported os", Advisory{Present: true, Count: 9, PkgMgr: "unsupported"}, false},
		{"empty pkgmgr", Advisory{Present: true, Count: 9, PkgMgr: ""}, false},
		{"real updates", Advisory{Present: true, Count: 7, PkgMgr: "apt"}, true},
	}
	for _, tc := range cases {
		if got := tc.a.HasUpdates(); got != tc.want {
			t.Errorf("%s: HasUpdates = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestShouldNotifyAggregate pins the once-a-day throttle: fire at most
// once per UTC day, and survive restarts (a stored date for today means
// don't re-fire even if the process restarts). Empty stored date = never
// notified → fire.
func TestShouldNotifyAggregate(t *testing.T) {
	t.Parallel()
	// Never notified → fire.
	if !shouldNotifyAggregate("2026-06-10", "") {
		t.Error("first run of the day should notify")
	}
	// Already notified today → don't re-fire (restart-safe, per-tick-safe).
	if shouldNotifyAggregate("2026-06-10", "2026-06-10") {
		t.Error("already-notified-today must not re-fire")
	}
	// A new day → fire again.
	if !shouldNotifyAggregate("2026-06-11", "2026-06-10") {
		t.Error("new day should notify")
	}
	// Empty today (defensive) → never fire.
	if shouldNotifyAggregate("", "2026-06-10") {
		t.Error("empty date must not fire")
	}
}

// TestAggregateTitleBody pins the digest copy: one event listing every
// node with pending updates, count-led, with a per-node line.
func TestAggregateTitleBody(t *testing.T) {
	t.Parallel()
	advs := []Advisory{
		{Node: "server2", Count: 11, RebootRequired: true, Sample: []string{"base-files 1->2"}},
		{Node: "tickero-node", Count: 4, RebootRequired: true},
	}
	_, body := aggregateTitleBody(advs)
	for _, want := range []string{
		"2 nodes with pending host package updates",
		"reboot required on some",
		"server2: 11 updates (reboot required)",
		"tickero-node: 4 updates (reboot required)",
		"base-files 1->2",
	} {
		if !contains(body, want) {
			t.Errorf("body = %q, want to contain %q", body, want)
		}
	}

	// Single node, singular wording, no reboot.
	_, body2 := aggregateTitleBody([]Advisory{{Node: "n", Count: 1}})
	if !contains(body2, "1 node with pending host package updates") {
		t.Errorf("single-node header wrong: %q", body2)
	}
	if contains(body2, "reboot required") {
		t.Errorf("should not mention reboot when none required: %q", body2)
	}
	if !contains(body2, "n: 1 update") {
		t.Errorf("singular per-node line wrong: %q", body2)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestParseApplyState(t *testing.T) {
	t.Parallel()
	if s := parseApplyState(""); s.Phase != "" {
		t.Errorf("empty → %+v, want zero", s)
	}
	if s := parseApplyState("{garbage"); s.Phase != "" {
		t.Errorf("malformed → %+v, want zero", s)
	}
	s := parseApplyState(`{"phase":"rebooting","at":"2026-06-01T00:00:00Z","log":"patched"}`)
	if s.Phase != "rebooting" || s.Log != "patched" {
		t.Errorf("parse: %+v", s)
	}
}
