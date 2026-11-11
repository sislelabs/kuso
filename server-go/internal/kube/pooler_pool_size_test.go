package kube

import (
	"strings"
	"testing"
)

// PgBouncer's default_pool_size is how many backend connections it will open
// per user/db pair. The chart hardcoded 25 — fine for an in-cluster postgres
// with max_connections=100, wrong for a managed backend: PlanetScale's plan
// gives ~9 usable, so under load the pooler itself exhausts the provider and
// clients get "too many connections". The pool size has to follow the
// backend's cap, and the reserve pool has to scale with it or it pushes the
// total right back over.
func TestKusoAddonChart_PoolerPoolSize(t *testing.T) {
	t.Parallel()
	// The unset default (25/5) is asserted in TestPooler_InClusterDefaults.
	out := helmTemplateAddon(t, "postgres",
		"external.secretName=creds", "pooler.enabled=true", "pooler.externalBackend=true",
		"pooler.host=aws.psdb.cloud", "pooler.poolSize=6")
	if !strings.Contains(out, "default_pool_size = 6") {
		t.Errorf("default_pool_size not applied.\n%s", out)
	}
	// 6/4 = 1 — a reserve of 5 on top of 6 would be 11, over the 9 cap.
	if !strings.Contains(out, "reserve_pool_size = 1") {
		t.Errorf("reserve_pool_size should scale with poolSize (want 1)")
	}
}
