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
	if got := s.buildkitHost(ctx, "shop"); got != buildkit.DefaultHost {
		t.Fatalf("no daemons: %q, want the Service", got)
	}

	s.Kube.Clientset = fake.NewClientset(
		daemon("bk-a", "kuso", "node-a", "10.42.1.3", true),
		daemon("bk-b", "kuso", "node-b", "10.42.2.3", false),
		daemon("bk-x", "other-ns", "node-c", "10.42.3.3", true),
	)
	for i := range 30 {
		p := fmt.Sprintf("p%d", i)
		if got := s.buildkitHost(ctx, p); got != "tcp://10.42.1.3:1234" {
			t.Fatalf("%s: got %q, want the only Ready daemon in the home namespace", p, got)
		}
	}

	s.Kube.Clientset = fake.NewClientset(
		daemon("bk-a", "kuso", "node-a", "10.42.1.3", true),
		daemon("bk-b", "kuso", "node-b", "10.42.2.3", true),
	)
	want, _ := buildkit.Pick("shop", []buildkit.Daemon{{Node: "node-a", IP: "10.42.1.3"}, {Node: "node-b", IP: "10.42.2.3"}})
	if got := s.buildkitHost(ctx, "shop"); got != want.Host() {
		t.Fatalf("got %q, want the project's rendezvous pick %q", got, want.Host())
	}
}
