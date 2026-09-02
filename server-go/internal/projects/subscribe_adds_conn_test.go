package projects

import (
	"slices"
	"testing"
)

// filterEnvFromForSubscription only ever REMOVES conns for addons a service is
// no longer subscribed to. It never adds one, so subscribing to an addon the
// env wasn't already mounting is a no-op on envFromSecrets: the name lands in
// spec.subscribedAddons, `kuso project addon list` shows a ✓, and the pod never
// receives a single one of the addon's keys.
//
// On tickero that surfaced as a blocked deploy — the migration release hook ran
// `migrate -database "$DIRECT_URL"` and got "URL cannot be empty", because
// tickero-psdb-conn was subscribed but never mounted.
//
// Unsubscribing worked, which is why the asymmetry went unnoticed: the filter
// is only exercised in the direction that removes.
func TestFilterEnvFromForSubscription_AddsNewlySubscribedConn(t *testing.T) {
	t.Parallel()

	// The env currently mounts only cache. The service just subscribed to psdb.
	current := []string{
		"tickero-cache-conn",
		"tickero-api-secrets",
	}
	subscribed := []string{"cache", "psdb"}
	projectAddons := []string{"tickero-cache-conn", "tickero-psdb-conn"}

	got := filterEnvFromForSubscription(current, subscribed, projectAddons, "tickero")

	if !slices.Contains(got, "tickero-cache-conn") {
		t.Errorf("dropped an already-mounted subscribed conn: %v", got)
	}
	if !slices.Contains(got, "tickero-api-secrets") {
		t.Errorf("dropped a non-addon secret: %v", got)
	}
	if !slices.Contains(got, "tickero-psdb-conn") {
		t.Errorf("subscribing to psdb did not mount its conn.\ngot = %v\n"+
			"the name is in subscribedAddons and the UI shows it subscribed, but "+
			"none of the addon's keys reach the pod", got)
	}
}

// The removing direction must keep working: an addon dropped from the
// subscription loses its mount.
func TestFilterEnvFromForSubscription_StillRemovesUnsubscribed(t *testing.T) {
	t.Parallel()

	current := []string{"tickero-cache-conn", "tickero-psdb-conn", "tickero-api-secrets"}
	subscribed := []string{"cache"}
	projectAddons := []string{"tickero-cache-conn", "tickero-psdb-conn"}

	got := filterEnvFromForSubscription(current, subscribed, projectAddons, "tickero")

	if slices.Contains(got, "tickero-psdb-conn") {
		t.Errorf("unsubscribed addon kept its mount: %v", got)
	}
	if !slices.Contains(got, "tickero-cache-conn") {
		t.Errorf("subscribed addon lost its mount: %v", got)
	}
}

// nil = legacy mount-all; the list must pass through untouched.
func TestFilterEnvFromForSubscription_NilIsUnchanged(t *testing.T) {
	t.Parallel()

	current := []string{"tickero-cache-conn", "tickero-api-secrets"}
	got := filterEnvFromForSubscription(current, nil, []string{"tickero-cache-conn", "tickero-psdb-conn"}, "tickero")

	if len(got) != len(current) {
		t.Errorf("nil subscription changed the list: %v", got)
	}
}
