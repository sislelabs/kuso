package kube

import (
	"regexp"
	"strings"
	"testing"
)

// The pooler's pgbouncer.ini is a subPath mount of a ConfigMap, so a re-render
// alone changes nothing in the running pod: PgBouncer keeps the config it
// started with until something else restarts it. Setting pooler.poolSize on a
// live addon therefore did nothing — the ConfigMap said 6, the pod ran 25.
// The pod template has to carry a hash of the ini so a config change rolls
// the Deployment.
func TestKusoAddonChart_PoolerConfigChangeRollsPod(t *testing.T) {
	t.Parallel()
	hashRe := regexp.MustCompile(`checksum/pooler-config: ([0-9a-f]{64})`)

	deploymentHash := func(out string) string {
		t.Helper()
		i := strings.Index(out, "kind: Deployment")
		if i < 0 {
			t.Fatalf("no Deployment rendered")
		}
		m := hashRe.FindStringSubmatch(out[i:])
		if m == nil {
			t.Fatalf("pooler Deployment pod template has no checksum/pooler-config annotation")
		}
		return m[1]
	}
	base := []string{"external.secretName=creds", "pooler.enabled=true",
		"pooler.externalBackend=true", "pooler.host=aws.psdb.cloud"}
	a := deploymentHash(helmTemplateAddon(t, "postgres", base...))
	b := deploymentHash(helmTemplateAddon(t, "postgres", append(base, "pooler.poolSize=6")...))
	if a == b {
		t.Errorf("changing pooler.poolSize left the pod template unchanged (hash %s) — the running PgBouncer would keep the old pool size", a)
	}
	c := deploymentHash(helmTemplateAddon(t, "postgres", base...))
	if a != c {
		t.Errorf("hash not stable across identical renders: %s vs %s", a, c)
	}
}
