package health

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
	"kuso/server/internal/notify"
)

// OPS-7/OPS-9: node disk fill pages from the kubelet reading (no DB), not
// on the first check, once per episode, and names the local-path platform
// volumes on that disk without blaming one of them.
func TestNodeDiskFillAlert(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}})
	w := New(&kube.Client{Clientset: cs}, testNS, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var events []notify.Event
	w.emit = func(e notify.Event) { events = append(events, e) }
	usedPct := int64(90)
	w.nodeDisks = func(context.Context, []*corev1.Node) map[string]kube.NodeFS {
		return map[string]kube.NodeFS{"n1": {CapacityBytes: 100 << 30, AvailableBytes: (100 - usedPct) << 30}}
	}
	onDisk := map[string][]string{"n1": {"Image registry (kuso-registry-data)", "Build cache (kuso-buildkitd-state)"}}
	ctx := context.Background()

	w.checkNodeDisks(ctx, onDisk)
	if len(events) != 0 {
		t.Fatalf("paged on the first check: %+v", events)
	}
	w.checkNodeDisks(ctx, onDisk)
	w.checkNodeDisks(ctx, onDisk)
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if !strings.Contains(events[0].Title, "n1 disk 90%") || !strings.Contains(events[0].Body, "kuso-registry-data") {
		t.Errorf("alert = %q / %q", events[0].Title, events[0].Body)
	}

	usedPct = 50
	w.checkNodeDisks(ctx, onDisk)
	usedPct = 90
	w.checkNodeDisks(ctx, onDisk)
	w.checkNodeDisks(ctx, onDisk)
	if len(events) != 2 {
		t.Fatalf("did not re-arm after the disk drained: %d events", len(events))
	}
}

// A PVCSelector entry (one claim per CNPG instance) expands to one check
// per claim, each read through a pod that actually mounts it.
func TestExpandPlatformVolumesBySelector(t *testing.T) {
	saved := platformVolumes
	t.Cleanup(func() { platformVolumes = saved })
	platformVolumes = []platformVolume{{PVCSelector: "cnpg.io/cluster=kuso-postgres", Label: "Postgres"}}
	pvc := func(name string) *corev1.PersistentVolumeClaim {
		return &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS,
			Labels: map[string]string{"cnpg.io/cluster": "kuso-postgres"}}}
	}
	cs := fake.NewSimpleClientset(pvc("kuso-postgres-1"), pvc("kuso-postgres-2"))
	w := New(&kube.Client{Clientset: cs}, testNS, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	got := w.expandPlatformVolumes(context.Background())
	if len(got) != 2 || got[0].Namespace != testNS || got[0].PVC == got[1].PVC {
		t.Fatalf("expanded = %+v", got)
	}
	pod := &corev1.Pod{Spec: corev1.PodSpec{Volumes: []corev1.Volume{{VolumeSource: corev1.VolumeSource{
		PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "kuso-postgres-2"}}}}}}
	if mountsClaim(pod, "kuso-postgres-1") || !mountsClaim(pod, "kuso-postgres-2") {
		t.Fatal("mountsClaim matched the wrong claim")
	}
}
