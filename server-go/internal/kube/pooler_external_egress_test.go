package kube

import (
	"strings"
	"testing"
)

// A pooler fronting an external provider dials out of the cluster, and the
// project's default-deny egress only lets pods carrying network-egress-public
// do that. Without the label PgBouncer starts, listens, and can never open a
// backend: every client waits query_wait_timeout and dies. This took tickero
// production down when the pooler pod was rolled.
func TestKusoAddonChart_ExternalPoolerGetsPublicEgress(t *testing.T) {
	t.Parallel()
	const label = `kuso.sislelabs.com/network-egress-public: "true"`

	podTemplate := func(out string) string {
		t.Helper()
		i := strings.Index(out, "kind: Deployment")
		if i < 0 {
			t.Fatalf("no pooler Deployment rendered")
		}
		j := strings.Index(out[i:], "  template:")
		if j < 0 {
			t.Fatalf("no pod template in pooler Deployment")
		}
		return out[i+j:]
	}

	ext := podTemplate(helmTemplateAddon(t, "postgres", "external.secretName=creds",
		"pooler.enabled=true", "pooler.externalBackend=true", "pooler.host=aws.psdb.cloud"))
	if !strings.Contains(ext, label) {
		t.Errorf("external-backend pooler pod template lacks %s — it can't reach the provider", label)
	}

	// In-cluster pooler talks only to the addon Service; keep it locked down.
	local := podTemplate(helmTemplateAddon(t, "postgres", "pooler.enabled=true"))
	if strings.Contains(local, label) {
		t.Errorf("in-cluster pooler must not get public egress")
	}
}
