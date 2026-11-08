package handlers

import "testing"

// The usage page counted 33 projects against the projects page's 28:
// samples are keyed by pod label, so deleted projects and the
// synthetic instance-addon project showed up as projects.
func TestClassifyUsageProject(t *testing.T) {
	live := map[string]bool{"analiz": true, "tickero": true}
	cases := []struct {
		name string
		live map[string]bool
		want string
	}{
		{"analiz", live, UsageKindProject},
		{"bugbash", live, UsageKindDeleted},
		{"kuso-instance", live, UsageKindPlatform},
		{"kuso-instance", nil, UsageKindPlatform},
		{"bugbash", nil, UsageKindProject}, // live list unknown: don't guess
	}
	for _, c := range cases {
		if got := classifyUsageProject(c.name, c.live); got != c.want {
			t.Errorf("classifyUsageProject(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}
