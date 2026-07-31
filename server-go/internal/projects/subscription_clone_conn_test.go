package projects

import (
	"sort"
	"testing"
)

// TestFilterEnvFromForSubscription_KeepsEnvScopedCloneConns reproduces the
// live incident: a subscription-enabled service with STAGING addon clones
// had its staging conn secrets (tickero-db-staging-conn, …) silently
// stripped when an env-var change propagated — the exact allow-set only
// held base names (tickero-db-conn), so the -staging-conn clones failed the
// filter and the staging pod crashed with no DATABASE_URL. Clones of a
// SUBSCRIBED base addon must be kept.
func TestFilterEnvFromForSubscription_KeepsEnvScopedCloneConns(t *testing.T) {
	t.Parallel()
	project := "tickero"
	subscribed := []string{"cache", "db", "queue", "storage"}
	// projectAddons = every project-owned conn (base + clones), which is
	// what listProjectAddonConnSecrets returns.
	projectAddons := []string{
		"tickero-cache-conn", "tickero-db-conn", "tickero-queue-conn", "tickero-storage-conn",
		"tickero-cache-staging-conn", "tickero-db-staging-conn", "tickero-queue-staging-conn", "tickero-storage-staging-conn",
	}
	// The staging env's envFromSecrets: its own clone conns + service secrets.
	in := []string{
		"tickero-cache-staging-conn", "tickero-db-staging-conn",
		"tickero-queue-staging-conn", "tickero-storage-staging-conn",
		"tickero-api-secrets", "tickero-api-staging-secrets",
	}
	got := filterEnvFromForSubscription(in, subscribed, projectAddons, project)
	sort.Strings(got)
	want := []string{
		"tickero-api-secrets", "tickero-api-staging-secrets",
		"tickero-cache-staging-conn", "tickero-db-staging-conn",
		"tickero-queue-staging-conn", "tickero-storage-staging-conn",
	}
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("filter dropped clone conns:\n got  %v\n want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("filter result mismatch:\n got  %v\n want %v", got, want)
		}
	}
}

// TestFilterEnvFromForSubscription_DropsUnsubscribed confirms the filter
// still WORKS: an UNsubscribed addon's conn (base or clone) is removed, and
// a subscribed-prefix lookalike ("database" vs subscribed "db") is not
// wrongly kept.
func TestFilterEnvFromForSubscription_DropsUnsubscribed(t *testing.T) {
	t.Parallel()
	project := "p"
	subscribed := []string{"db"}
	projectAddons := []string{"p-db-conn", "p-db-staging-conn", "p-cache-conn", "p-database-conn"}
	in := []string{
		"p-db-conn",          // subscribed base → keep
		"p-db-staging-conn",  // subscribed clone → keep
		"p-cache-conn",       // NOT subscribed → drop
		"p-database-conn",    // prefix lookalike, NOT "db-<scope>" → drop
		"p-shared",           // not an addon conn → keep
	}
	got := filterEnvFromForSubscription(in, subscribed, projectAddons, project)
	keep := map[string]bool{}
	for _, s := range got {
		keep[s] = true
	}
	if !keep["p-db-conn"] || !keep["p-db-staging-conn"] || !keep["p-shared"] {
		t.Errorf("subscribed base/clone or non-addon secret wrongly dropped: %v", got)
	}
	if keep["p-cache-conn"] {
		t.Errorf("unsubscribed p-cache-conn should be dropped: %v", got)
	}
	if keep["p-database-conn"] {
		t.Errorf("prefix lookalike p-database-conn should NOT match subscribed 'db': %v", got)
	}
}
