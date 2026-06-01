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

// TestShouldNotify pins the restart-safe edge-dedup: notify only when
// there are real updates AND the advisory is newer than what we last
// announced. This is what stops re-paging on every server restart.
func TestShouldNotify(t *testing.T) {
	t.Parallel()
	fresh := Advisory{Present: true, Count: 7, PkgMgr: "apt", CheckedAt: "2026-06-01T18:00:00Z"}

	// Never notified before → fire.
	if !shouldNotify(fresh, "") {
		t.Error("first advisory should notify")
	}
	// Same checkedAt already notified → don't re-fire (restart-safe).
	if shouldNotify(fresh, "2026-06-01T18:00:00Z") {
		t.Error("already-notified advisory must not re-fire")
	}
	// Older than what we notified (clock skew / stale read) → don't fire.
	if shouldNotify(fresh, "2026-06-01T19:00:00Z") {
		t.Error("stale advisory must not fire")
	}
	// A newer probe run → fire again.
	newer := fresh
	newer.CheckedAt = "2026-06-02T00:00:00Z"
	if !shouldNotify(newer, "2026-06-01T18:00:00Z") {
		t.Error("newer advisory should re-notify")
	}
	// No actionable updates → never fire regardless of recency.
	if shouldNotify(Advisory{Present: true, Count: 0, PkgMgr: "apt", CheckedAt: "2026-06-03T00:00:00Z"}, "") {
		t.Error("zero-count advisory must not notify")
	}
}

func TestNotifyTitleBody(t *testing.T) {
	t.Parallel()
	_, body := notifyTitleBody(Advisory{Node: "server2", Count: 50, RebootRequired: true, Sample: []string{"base-files 1->2"}})
	if want := "Node server2: 50 package updates available (reboot required)"; !contains(body, want) {
		t.Errorf("body = %q, want to contain %q", body, want)
	}
	// Singular + no reboot.
	_, body2 := notifyTitleBody(Advisory{Node: "n", Count: 1})
	if !contains(body2, "1 package update available") || contains(body2, "reboot required") {
		t.Errorf("singular/no-reboot body wrong: %q", body2)
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
