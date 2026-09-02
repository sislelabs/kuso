package kube

import (
	"strings"
	"testing"
)

// max_client_conn caps how many app connections the pooler accepts, not how
// many hit the database. Every app pod opens its own client pool (tickero: 25
// per pod), so 200 was a wall at ~8 pods. It must comfortably exceed a
// realistic pod count times a per-pod pool.
func TestKusoAddonChart_PoolerClientCapAllowsManyPods(t *testing.T) {
	t.Parallel()
	out := helmTemplateAddon(t, "postgres", "pooler.enabled=true")
	if !strings.Contains(out, "max_client_conn = 1000") {
		t.Errorf("max_client_conn should be 1000 (40 pods × 25 client conns); got:\n%s", grepLine(out, "max_client_conn"))
	}
}

func grepLine(s, needle string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, needle) {
			return strings.TrimSpace(l)
		}
	}
	return "<absent>"
}
