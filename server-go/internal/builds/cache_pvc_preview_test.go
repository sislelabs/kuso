package builds

import (
	"context"
	"log/slog"
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

// dockerfile/nixpacks compile in buildkitd and never read the cache PVC;
// creating one only pins later builds to the node it bound on.
func TestCreate_CachePVCOnlyForInPodStrategies(t *testing.T) {
	t.Parallel()
	const ref = "abcdef0123456789abcdef0123456789abcdef01"
	for runtime, want := range map[string]bool{"": false, "dockerfile": false, "nixpacks": false, "worker": false, "static": true} {
		svc := seedService("alpha", "web")
		svc.obj.Object["spec"].(map[string]any)["runtime"] = runtime
		s := fakeService(t, seedProject("alpha", "main", "https://github.com/example/alpha", 0), svc)
		got, err := s.Create(context.Background(), "alpha", "web", CreateBuildRequest{Ref: ref})
		if err != nil {
			t.Fatalf("%q: Create: %v", runtime, err)
		}
		if has := got.Spec.Cache != nil && got.Spec.Cache.PVCName != ""; has != want {
			t.Errorf("%q: spec.cache = %+v, want attached=%v", runtime, got.Spec.Cache, want)
		}
		pvcs, err := s.Kube.Clientset.CoreV1().PersistentVolumeClaims("kuso").List(context.Background(), metav1.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if created := len(pvcs.Items) > 0; created != want {
			t.Errorf("%q: %d cache PVCs created, want created=%v", runtime, len(pvcs.Items), want)
		}
	}
}

// The queued-build promote path makes the same strategy decision.
func TestPromoteOne_CachePVCOnlyForInPodStrategies(t *testing.T) {
	t.Parallel()
	for strategy, want := range map[string]bool{"": false, "dockerfile": false, "nixpacks": false, "static": true} {
		b := &kube.KusoBuild{
			ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-q", Namespace: "kuso",
				Labels: map[string]string{"kuso.sislelabs.com/build-state": "queued"}},
			Spec: kube.KusoBuildSpec{Project: "alpha", Service: "alpha-web", Ref: cloneTokenTestRef, Strategy: strategy},
		}
		s := fakeService(t, seedService("alpha", "web"), seedBuild(b))
		p := &Poller{Svc: s, Logger: slog.Default()}
		if !p.promoteOne(context.Background(), "kuso", "alpha", "alpha-web", b) {
			t.Fatalf("%q: promoteOne returned false", strategy)
		}
		got, err := s.Kube.GetKusoBuild(context.Background(), "kuso", "alpha-web-q")
		if err != nil {
			t.Fatal(err)
		}
		if has := got.Spec.Cache != nil && got.Spec.Cache.PVCName != ""; has != want {
			t.Errorf("%q: promoted spec.cache = %+v, want attached=%v", strategy, got.Spec.Cache, want)
		}
	}
}
