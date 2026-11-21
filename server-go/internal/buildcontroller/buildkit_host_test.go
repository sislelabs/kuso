package buildcontroller

import (
	"context"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/buildkit"
	"kuso/server/internal/kube"
)

func TestBuildkitHostDialsTheProjectsReadyDaemon(t *testing.T) {
	daemon := func(name, ns, node, ip string, ready bool) *corev1.Pod {
		st := corev1.ConditionTrue
		if !ready {
			st = corev1.ConditionFalse
		}
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{"app.kubernetes.io/name": "kuso-buildkitd"}},
			Spec:       corev1.PodSpec{NodeName: node},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: ip,
				Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: st}}},
		}
	}
	ctx := context.Background()
	s := &Service{Kube: &kube.Client{Clientset: fake.NewClientset()}, Namespace: "kuso"}
	if got, node := s.buildkitHost(ctx, "shop"); got != buildkit.DefaultHost || node != "" {
		t.Fatalf("no daemons: %q on %q, want the Service and no node", got, node)
	}

	s.Kube.Clientset = fake.NewClientset(
		daemon("bk-a", "kuso", "node-a", "10.42.1.3", true),
		daemon("bk-b", "kuso", "node-b", "10.42.2.3", false),
		daemon("bk-x", "other-ns", "node-c", "10.42.3.3", true),
	)
	for i := range 30 {
		p := fmt.Sprintf("p%d", i)
		if got, node := s.buildkitHost(ctx, p); got != "tcp://10.42.1.3:1234" || node != "node-a" {
			t.Fatalf("%s: got %q on %q, want the only Ready daemon in the home namespace", p, got, node)
		}
	}

	s.Kube.Clientset = fake.NewClientset(
		daemon("bk-a", "kuso", "node-a", "10.42.1.3", true),
		daemon("bk-b", "kuso", "node-b", "10.42.2.3", true),
	)
	want, _ := buildkit.Pick("shop", []buildkit.Daemon{{Node: "node-a", IP: "10.42.1.3"}, {Node: "node-b", IP: "10.42.2.3"}})
	if got, node := s.buildkitHost(ctx, "shop"); got != want.Host() || node != want.Node {
		t.Fatalf("got %q on %q, want the project's rendezvous pick %q on %q", got, node, want.Host(), want.Node)
	}
}
