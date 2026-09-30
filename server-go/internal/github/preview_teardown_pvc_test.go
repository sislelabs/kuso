package github

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A closed PR must reclaim its service-volume PVCs. They carry
// helm.sh/resource-policy=keep, and preview env names are deterministic,
// so a leftover <svc>-pr-N-<vol> is mounted verbatim by the next PR #N.
func TestDispatch_PRClosed_ReclaimsPreviewVolumePVCs(t *testing.T) {
	t.Parallel()
	d := newDispatcher(t,
		seedProj("alpha", "https://github.com/example/alpha", "main", true, 5),
		seedSvc("alpha", "web"),
		seedPreviewEnv("alpha", "web", 42, "feat/x"),
	)
	ctx := context.Background()
	pvc := func(name, instance string) *corev1.PersistentVolumeClaim {
		return &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "kuso",
			Labels: map[string]string{
				"app.kubernetes.io/instance": instance,
				"kuso.sislelabs.com/volume":  "data",
			},
		}}
	}
	for _, p := range []*corev1.PersistentVolumeClaim{
		pvc("alpha-web-pr-42-data", "alpha-web-pr-42"),
		pvc("alpha-web-pr-43-data", "alpha-web-pr-43"),
		pvc("alpha-web-data", "alpha-web-production"),
	} {
		if _, err := d.Kube.Clientset.CoreV1().PersistentVolumeClaims("kuso").Create(ctx, p, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seed pvc: %v", err)
		}
	}

	body := []byte(`{
		"action": "closed",
		"number": 42,
		"pull_request": {"head": {"ref": "feat/x", "sha": "abc"}, "state": "closed"},
		"repository": {"full_name": "example/alpha"}
	}`)
	if err := d.Dispatch(ctx, "pull_request", body); err != nil {
		t.Fatalf("Dispatch pr closed: %v", err)
	}

	left, err := d.Kube.Clientset.CoreV1().PersistentVolumeClaims("kuso").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, p := range left.Items {
		got[p.Name] = true
	}
	if got["alpha-web-pr-42-data"] {
		t.Error("PR #42 volume PVC survived PR close")
	}
	if !got["alpha-web-pr-43-data"] || !got["alpha-web-data"] {
		t.Errorf("PR close deleted another env's volume PVC; left=%v", got)
	}
}
