package nodewatch

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
	"kuso/server/internal/pkgupdates"
)

func unknownNode(name string, since time.Time, ann map[string]string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: ann},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
			{Type: corev1.NodeReady, Status: corev1.ConditionUnknown, LastTransitionTime: metav1.NewTime(since)},
		}},
	}
}

func nodePod(name, node, app string, rsOwned bool, pvc bool) *corev1.Pod {
	ctrl := true
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso",
			Labels: map[string]string{"app.kubernetes.io/name": app}},
		Spec:   corev1.PodSpec{NodeName: node},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	if rsOwned {
		p.OwnerReferences = []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "rs", Controller: &ctrl}}
	}
	if pvc {
		p.Spec.Volumes = []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}}}}
	}
	if app == "kusoaddon" {
		p.Labels["kuso.sislelabs.com/addon"] = "pg"
	}
	return p
}

// RUN-8: once a node has been unreachable past the eviction window, its
// stateless service pods are deleted so their ReplicaSets reschedule them
// now instead of after Kubernetes' 300s taint eviction. Volume-backed,
// addon and non-kuso pods are left alone.
func TestTickEvictsStatelessPodsFromUnreachableNode(t *testing.T) {
	t.Parallel()
	now := time.Now()
	cs := kubefake.NewSimpleClientset(
		unknownNode("dead", now.Add(-2*time.Minute), nil),
		nodePod("web", "dead", "kusoenvironment", true, false),
		nodePod("vol", "dead", "kusoenvironment", true, true),
		nodePod("pg-0", "dead", "kusoaddon", false, true),
		nodePod("other", "dead", "traefik", true, false),
		nodePod("web-elsewhere", "alive", "kusoenvironment", true, false),
		nodePod("act", "dead", "kuso-activator", true, false),
		inNS(nodePod("traefik-1", "dead", "traefik", true, false), "traefik"),
		inNS(nodePod("tenant-act", "dead", "kuso-activator", true, false), "tenant"),
	)
	w := &Watcher{Kube: &kube.Client{Clientset: cs}, Logger: slogDiscard(),
		notReadySince: map[string]time.Time{}, alerted: map[string]struct{}{}}
	w.tick(context.Background())

	left := map[string]bool{}
	pods, _ := cs.CoreV1().Pods("kuso").List(context.Background(), metav1.ListOptions{})
	for _, p := range pods.Items {
		left[p.Name] = true
	}
	if left["web"] || left["act"] {
		t.Error("stateless service / activator pod on the dead node was not evicted")
	}
	if _, err := cs.CoreV1().Pods("traefik").Get(context.Background(), "traefik-1", metav1.GetOptions{}); err == nil {
		t.Error("traefik pod on the dead node was not evicted")
	}
	if _, err := cs.CoreV1().Pods("tenant").Get(context.Background(), "tenant-act", metav1.GetOptions{}); err != nil {
		t.Error("a tenant pod merely labelled kuso-activator must not be evicted")
	}
	for _, keep := range []string{"vol", "pg-0", "other", "web-elsewhere"} {
		if !left[keep] {
			t.Errorf("pod %s must not be evicted", keep)
		}
	}
}

func inNS(p *corev1.Pod, ns string) *corev1.Pod {
	p.Namespace = ns
	return p
}

func TestShouldEvict(t *testing.T) {
	t.Parallel()
	now := time.Now()
	notReadyFalse := notReadyNode("n", false, nil) // kubelet alive, reporting NotReady
	cases := []struct {
		name  string
		node  *corev1.Node
		after time.Duration
		want  bool
	}{
		{"unknown past window", unknownNode("n", now.Add(-90*time.Second), nil), time.Minute, true},
		{"unknown inside window", unknownNode("n", now.Add(-30*time.Second), nil), time.Minute, false},
		{"Ready=False is not unreachable", notReadyFalse, time.Minute, false},
		{"planned reboot", unknownNode("n", now.Add(-10*time.Minute), map[string]string{pkgupdates.CordonAnnotation: "true"}), time.Minute, false},
		{"disabled", unknownNode("n", now.Add(-10*time.Minute), nil), 0, false},
	}
	for _, c := range cases {
		if got := shouldEvict(c.node, now, c.after); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestAddonGuidanceNamesStrandedAddons(t *testing.T) {
	t.Parallel()
	pods := []corev1.Pod{*nodePod("pg-0", "dead", "kusoaddon", false, true), *nodePod("web", "dead", "kusoenvironment", true, false)}
	got := addonGuidance(addonsOnNode(pods))
	if !strings.Contains(got, "kuso/pg") {
		t.Errorf("guidance %q does not name the stranded addon", got)
	}
	if addonGuidance(nil) != "" {
		t.Error("no addons must add no text")
	}
}
