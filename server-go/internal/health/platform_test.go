package health

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
	"kuso/server/internal/notify"
)

type platformHarness struct {
	w      *Watcher
	cs     *fake.Clientset
	now    time.Time
	events []notify.Event
}

func newPlatformHarness(t *testing.T) *platformHarness {
	t.Helper()
	one := int32(1)
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "traefik", Namespace: "traefik"},
		Spec: appsv1.DeploymentSpec{
			Replicas: &one,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "traefik"}},
		},
	}
	h := &platformHarness{cs: fake.NewSimpleClientset(dep), now: time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC)}
	h.w = New(&kube.Client{Clientset: h.cs}, testNS, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.w.now = func() time.Time { return h.now }
	h.w.emit = func(e notify.Event) { h.events = append(h.events, e) }
	return h
}

func traefikPod(waiting string, ready bool, restarts int32, lastReason string) *corev1.Pod {
	st := corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
	if waiting != "" {
		st = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: waiting}}
	}
	cond := corev1.ConditionFalse
	if ready {
		cond = corev1.ConditionTrue
	}
	cs := corev1.ContainerStatus{Name: "traefik", State: st, RestartCount: restarts}
	if lastReason != "" {
		cs.LastTerminationState = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: lastReason, ExitCode: 137}}
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "traefik-abc", Namespace: "traefik", UID: "u1",
			Labels: map[string]string{"app.kubernetes.io/name": "traefik"}},
		Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: cond}},
			ContainerStatuses: []corev1.ContainerStatus{cs},
		},
	}
}

// tick replaces the Traefik pod, runs one platform check, advances 1m.
func (h *platformHarness) tick(t *testing.T, p *corev1.Pod) {
	t.Helper()
	ctx := context.Background()
	_ = h.cs.CoreV1().Pods("traefik").Delete(ctx, p.Name, metav1.DeleteOptions{})
	if _, err := h.cs.CoreV1().Pods("traefik").Create(ctx, p, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	h.w.checkPlatform(ctx)
	h.now = h.now.Add(time.Minute)
}

// A crashlooping Traefik pages once after the grace (never on the first
// sighting) and recovers once after a stable healthy window.
func TestPlatformCrashloopPagesOnceAndRecovers(t *testing.T) {
	h := newPlatformHarness(t)
	h.tick(t, traefikPod("CrashLoopBackOff", false, 4, "OOMKilled"))
	if len(h.events) != 0 {
		t.Fatalf("paged on first sighting: %+v", h.events)
	}
	for i := 0; i < 5; i++ {
		h.tick(t, traefikPod("CrashLoopBackOff", false, 4, "OOMKilled"))
	}
	if n := countType(h.events, notify.EventPlatformUnhealthy); n != 1 {
		t.Fatalf("platform.unhealthy = %d, want 1", n)
	}
	if got := h.events[0].Extra["component"]; got != "Traefik" {
		t.Fatalf("component = %q", got)
	}
	for i := 0; i < healthyTicks+1; i++ {
		h.tick(t, traefikPod("", true, 4, ""))
	}
	if n := countType(h.events, notify.EventPlatformRecovered); n != 1 {
		t.Fatalf("platform.recovered = %d, want 1", n)
	}
}

// Repeated OOM kills page even when the pod is Ready at every tick.
// Restarts that predate the first sighting don't count.
func TestPlatformRepeatedOOMPages(t *testing.T) {
	h := newPlatformHarness(t)
	h.tick(t, traefikPod("", true, 7, "OOMKilled")) // seeds at 7
	h.tick(t, traefikPod("", true, 7, "OOMKilled"))
	if len(h.events) != 0 {
		t.Fatalf("pre-existing restarts paged: %+v", h.events)
	}
	h.tick(t, traefikPod("", true, 9, "OOMKilled"))
	h.tick(t, traefikPod("", true, 10, "OOMKilled")) // 3 OOMs in window; episode opens
	for i := 0; i < 3; i++ {
		h.tick(t, traefikPod("", true, 10+int32(i+1), "OOMKilled"))
	}
	if n := countType(h.events, notify.EventPlatformUnhealthy); n != 1 {
		t.Fatalf("platform.unhealthy = %d, want 1", n)
	}
}

// A platform workload that doesn't exist on this install is skipped.
func TestPlatformMissingWorkloadIgnored(t *testing.T) {
	h := newPlatformHarness(t)
	for i := 0; i < 10; i++ {
		h.w.checkPlatform(context.Background())
		h.now = h.now.Add(time.Minute)
	}
	// Traefik has a Deployment but no pods: that IS down.
	if n := countType(h.events, notify.EventPlatformUnhealthy); n != 1 {
		t.Fatalf("platform.unhealthy = %d, want 1 (traefik, no pods); others must be skipped", n)
	}
}
