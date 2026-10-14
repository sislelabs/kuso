package builds

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// B-07: during a wedged rollout the old ReplicaSet keeps serving an
// image no env references; a KusoRun can likewise pin an old tag. The
// window sweep must not untag either.
func TestSweepImagesPastWindow_ProtectsPodAndRunImages(t *testing.T) {
	t.Parallel()
	seedB := func(name, tag string, sec int64) seed {
		return typedSeed(kube.GVRBuilds, "KusoBuild", &kube.KusoBuild{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: "kuso",
				CreationTimestamp: metav1.NewTime(time.Unix(sec, 0)),
				Labels:            map[string]string{"kuso.sislelabs.com/build-state": "done"},
				Annotations:       map[string]string{annPhase: "succeeded"},
			},
			Spec: kube.KusoBuildSpec{
				Project: "alpha", Service: "alpha-web",
				Image: &kube.KusoImage{Repository: "reg.local:5000/alpha/web", Tag: tag},
			},
		})
	}
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "web"),
		typedSeed(kube.GVRRuns, "KusoRun", &kube.KusoRun{
			ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-run", Namespace: "kuso"},
			Spec: kube.KusoRunSpec{
				Image: &kube.KusoImage{Repository: "reg.local:5000/alpha/web", Tag: "runtag00000"},
			},
		}),
		seedB("alpha-web-t0", "podtag00000", 100),
		seedB("alpha-web-run", "runtag00000", 200),
		seedB("alpha-web-gone", "gonetag0000", 300),
		seedB("alpha-web-t5", "newtag11111", 900),
	)
	if _, err := s.Kube.Clientset.CoreV1().Pods("kuso").Create(context.Background(), &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-old-rs-abc", Namespace: "kuso"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "reg.local:5000/alpha/web:podtag00000"}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	del := &fakeDeleter{}
	if _, err := SweepImagesPastWindow(context.Background(), s.Kube, "kuso", nil, del, 1, nil); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	got := map[string]bool{}
	for _, d := range del.deleted {
		got[d] = true
	}
	if got["alpha/web:podtag00000"] {
		t.Error("sweep untagged the image a running pod still serves")
	}
	if got["alpha/web:runtag00000"] {
		t.Error("sweep untagged the image a KusoRun references")
	}
	if !got["alpha/web:gonetag0000"] {
		t.Errorf("unreferenced old tag should still be swept; deleted=%v", del.deleted)
	}
}
