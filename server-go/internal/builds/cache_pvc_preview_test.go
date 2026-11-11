package builds

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// ISO-8: preview builds run PR code with the cache mounted read-write, so
// they must never get the production build-cache PVC.
func TestEnsureBuildCachePVC_PreviewGetsOwnPVC(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewSimpleClientset()
	s := &Service{Kube: &kube.Client{Clientset: cs}}
	prod := s.ensureBuildCachePVC(ctx, "ns", "alpha-api", nil, 5, false)
	prev := s.ensureBuildCachePVC(ctx, "ns", "alpha-api", nil, 5, true)
	if prod != "alpha-api-build-cache" || prev != "alpha-api-build-cache-preview" {
		t.Fatalf("prod=%q preview=%q", prod, prev)
	}
	for _, n := range []string{prod, prev} {
		if _, err := cs.CoreV1().PersistentVolumeClaims("ns").Get(ctx, n, metav1.GetOptions{}); err != nil {
			t.Fatalf("pvc %s: %v", n, err)
		}
	}
}
