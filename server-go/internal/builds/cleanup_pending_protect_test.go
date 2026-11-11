package builds

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// A held rollout's spec.pendingImage runs nowhere yet, but approving the
// hold pulls it. Newer builds must not push it out of the window (BLOB-9).
func TestSweepImagesPastWindow_ProtectsPendingImage(t *testing.T) {
	t.Parallel()
	const reg = "kuso-registry.kuso.svc.cluster.local:5000"
	s := fakeService(t, typedSeed(kube.GVREnvironments, "KusoEnvironment", &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-production", Namespace: "kuso"},
		Spec: kube.KusoEnvironmentSpec{
			Project: "alpha", Service: "alpha-web", Kind: "production",
			Image:        &kube.KusoImage{Repository: reg + "/alpha/web", Tag: "live3"},
			PendingImage: &kube.KusoImage{Repository: reg + "/alpha/web", Tag: "held0"},
		},
	}))
	rows := []ArchivedImageRecord{
		{BuildName: "alpha-web-held0", Project: "alpha", Service: "web", ImageTag: "held0", Status: "succeeded", CreatedAt: time.Unix(100, 0)},
		{BuildName: "alpha-web-live1", Project: "alpha", Service: "web", ImageTag: "live1", Status: "succeeded", CreatedAt: time.Unix(200, 0)},
		{BuildName: "alpha-web-live2", Project: "alpha", Service: "web", ImageTag: "live2", Status: "succeeded", CreatedAt: time.Unix(300, 0)},
		{BuildName: "alpha-web-live3", Project: "alpha", Service: "web", ImageTag: "live3", Status: "succeeded", CreatedAt: time.Unix(400, 0)},
	}
	del := &fakeDeleter{digests: map[string]string{}}
	for _, r := range rows {
		del.digests["alpha/web:"+r.ImageTag] = "sha256:" + r.ImageTag
	}
	if _, err := SweepImagesPastWindow(context.Background(), s.Kube, "kuso", &fakeArchivedLister{rows: rows}, del, 1, nil); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	deleted := map[string]bool{}
	for _, d := range del.deleted {
		deleted[d] = true
	}
	if deleted["alpha/web:held0"] {
		t.Errorf("sweep untagged the held pendingImage; deleted=%v", del.deleted)
	}
	if !deleted["alpha/web:live1"] {
		t.Errorf("sweep kept an unreferenced old tag; deleted=%v (a no-op sweep would pass)", del.deleted)
	}
}
