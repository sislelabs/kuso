package kusoCli

import "testing"

// F10: `build why` skipped release-failed builds ("no failed builds …
// nothing to explain") while the newest build was one.
func TestIsFailedStatus_IncludesReleaseFailed(t *testing.T) {
	for status, want := range map[string]bool{
		"failed":         true,
		"error":          true,
		"release-failed": true,
		"succeeded":      false,
		"cancelled":      false,
		"running":        false,
	} {
		if got := isFailedStatus(status); got != want {
			t.Errorf("isFailedStatus(%q) = %v, want %v", status, got, want)
		}
	}
}
