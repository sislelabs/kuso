package previewdb

import "testing"

// TestIsPreviewCloneName locks in the addon-clone idempotency fix
// from v0.17.6. EnsurePRAddons used to call Addons.List then clone
// every postgres addon it saw — including addons that were
// themselves clones from a previous PR sync — producing names like
// "tickero-pg-pr-35-pr-35-pr-35-pr-35". This regex is the filter
// that breaks that loop.
func TestIsPreviewCloneName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"source addon", "tickero-pg", false},
		{"source with hyphens", "tickero-prod-db", false},
		{"normal clone", "tickero-pg-pr-35", true},
		{"normal clone single-segment source", "pg-pr-42", true},
		{"double-cloned (the bug case)", "tickero-pg-pr-35-pr-35", true},
		{"triple-cloned", "tickero-pg-pr-35-pr-35-pr-35", true},
		{"different PR numbers (still a clone)", "tickero-pg-pr-1-pr-2", true},
		// Edge cases that look like clones but aren't.
		{"pr in middle of name (not suffix)", "tickero-pr-team-db", false},
		{"pr suffix without number", "tickero-pg-pr", false},
		{"non-numeric suffix", "tickero-pg-pr-abc", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := isPreviewCloneName(tc.in)
			if got != tc.want {
				t.Errorf("isPreviewCloneName(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
