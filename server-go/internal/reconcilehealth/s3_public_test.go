package reconcilehealth

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

func TestClassifyS3PublicTCP(t *testing.T) {
	t.Parallel()
	mk := func(kind string, pt *kube.KusoAddonPublicTCP) *kube.KusoAddon {
		return &kube.KusoAddon{ObjectMeta: metav1.ObjectMeta{Name: "shop-files", Namespace: "kuso"},
			Spec: kube.KusoAddonSpec{Kind: kind, Project: "shop", PublicTCP: pt}}
	}
	on := &kube.KusoAddonPublicTCP{Enabled: true, Port: 30001}
	iss, ok := ClassifyS3PublicTCP(mk("s3", on))
	if !ok || iss.Kind != KindS3PublicPlaintext || iss.RunbookCmd != "kuso project addon public-tcp disable shop files" {
		t.Errorf("exposed s3: ok=%v issue=%+v", ok, iss)
	}
	for _, a := range []*kube.KusoAddon{mk("s3", nil), mk("postgres", on), mk("s3", &kube.KusoAddonPublicTCP{})} {
		if _, ok := ClassifyS3PublicTCP(a); ok {
			t.Errorf("flagged %s publicTCP=%+v", a.Spec.Kind, a.Spec.PublicTCP)
		}
	}
}
