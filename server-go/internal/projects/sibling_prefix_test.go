package projects

import (
	"slices"
	"testing"
)

// "storage-archive" is a separate project addon, not a clone of "storage".
// The filter must neither keep it for a "storage" subscriber nor treat it as
// a clone that blocks mounting the real tk-storage-conn.
func TestFilterEnvFromForSubscription_SiblingWithSubscribedPrefix(t *testing.T) {
	t.Parallel()
	projectAddons := []string{"tk-storage-archive-conn", "tk-storage-conn"}

	got := filterEnvFromForSubscription(
		[]string{"tk-storage-conn", "tk-storage-archive-conn", "tk-api-secrets"},
		[]string{"storage"}, projectAddons, "tk")
	if slices.Contains(got, "tk-storage-archive-conn") {
		t.Errorf("unsubscribed sibling kept: %v", got)
	}

	// Subscribing to both while only the sibling is mounted must add the
	// real conn, not skip it as if the sibling were its clone.
	got = filterEnvFromForSubscription(
		[]string{"tk-storage-archive-conn", "tk-api-secrets"},
		[]string{"storage", "storage-archive"}, projectAddons, "tk")
	if !slices.Contains(got, "tk-storage-conn") {
		t.Errorf("subscribed tk-storage-conn not added while the sibling was mounted: %v", got)
	}
}
