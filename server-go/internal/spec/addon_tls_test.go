package spec

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// TestAddonTLSRoundTrip pins the kuso.yml surface for addon tls: a
// `tls: require` entry in the YAML reaches the create request, and a
// live tls=require CR exports back to the same YAML — so export→apply
// round-trips without silently downgrading the DB to plaintext.
func TestAddonTLSRoundTrip(t *testing.T) {
	t.Parallel()

	req := addonCreateReq(AddonSpec{Name: "db", Kind: "postgres", TLS: "require"})
	if req.TLS != "require" {
		t.Errorf("addonCreateReq tls = %q, want require", req.TLS)
	}

	exported := exportAddon("shop", kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{Name: "shop-db", Namespace: "kuso"},
		Spec:       kube.KusoAddonSpec{Project: "shop", Kind: "postgres", TLS: "require"},
	})
	if exported.TLS != "require" {
		t.Errorf("exportAddon tls = %q, want require", exported.TLS)
	}
}
