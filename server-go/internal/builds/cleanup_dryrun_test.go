package builds

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// BUILD-11: dry-run builds push nothing, so they must not take slots in the
// keep-newest-N window. N dry runs used to evict every real rollback target.
func TestSweepImagesPastWindow_IgnoresDryRuns(t *testing.T) {
	t.Parallel()
	b := func(name, tag string, at int64, dry bool) seed {
		return seedBuild(&kube.KusoBuild{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: "kuso", CreationTimestamp: metav1.NewTime(time.Unix(at, 0)),
				Labels:      map[string]string{"kuso.sislelabs.com/build-state": "done", "kuso.sislelabs.com/project": "alpha"},
				Annotations: map[string]string{annPhase: "succeeded"},
			},
			Spec: kube.KusoBuildSpec{Project: "alpha", Service: "alpha-web", DryRun: dry, Done: true,
				Image: &kube.KusoImage{Repository: "kuso-registry.kuso.svc.cluster.local:5000/alpha/web", Tag: tag}},
		})
	}
	s := fakeService(t,
		b("alpha-web-real", "real", 100, false),
		b("alpha-web-dry1", "dry1", 200, true),
		b("alpha-web-dry2", "dry2", 300, true),
	)
	del := &fakeDeleter{digests: map[string]string{}}
	if _, err := SweepImagesPastWindow(context.Background(), s.Kube, "kuso", nil, del, 1, nil); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	for _, d := range del.deleted {
		if d == "alpha/web:real" {
			t.Errorf("newest real image swept to keep dry runs in the window (deleted=%v)", del.deleted)
		}
	}
}
