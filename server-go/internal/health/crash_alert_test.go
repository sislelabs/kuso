package health

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
	"kuso/server/internal/notify"
)

const testNS = "kuso"

type crashHarness struct {
	w      *Watcher
	cs     *fake.Clientset
	now    time.Time
	events []notify.Event
}

// newCrashHarness seeds the production env CR of service "api" in project
// "shop", named the way the platform names it.
func newCrashHarness(t *testing.T) *crashHarness {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{kube.GVREnvironments: "KusoEnvironmentList"})
	env := &unstructured.Unstructured{}
	env.SetGroupVersionKind(schema.GroupVersionKind{
		Group: kube.GVREnvironments.Group, Version: kube.GVREnvironments.Version, Kind: "KusoEnvironment",
	})
	env.SetNamespace(testNS)
	env.SetName("shop-api-production")
	env.SetLabels(map[string]string{kube.LabelProject: "shop", kube.LabelService: "api", kube.LabelEnv: "production"})
	if err := dyn.Tracker().Create(kube.GVREnvironments, env, testNS); err != nil {
		t.Fatal(err)
	}
	h := &crashHarness{cs: fake.NewSimpleClientset(), now: time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)}
	h.w = New(&kube.Client{Clientset: h.cs, Dynamic: dyn}, testNS, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.w.now = func() time.Time { return h.now }
	h.w.emit = func(e notify.Event) { h.events = append(h.events, e) }
	return h
}

func svcPod(name, reason string) *corev1.Pod {
	st := corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
	ready := corev1.ConditionTrue
	if reason != "" {
		st = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason}}
		ready = corev1.ConditionFalse
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: testNS,
			Labels: map[string]string{
				"app.kubernetes.io/name":      "kusoenvironment",
				"app.kubernetes.io/instance":  "shop-api-production",
				kube.LabelProject:             "shop",
				kube.LabelService:             "shop-api", // chart stamps the FQN
				"kuso.sislelabs.com/env-kind": "production",
			},
		},
		Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: ready}},
			ContainerStatuses: []corev1.ContainerStatus{{Name: "app", State: st}},
		},
	}
}

// tick replaces the pod set, runs checkPods, then advances the clock.
func (h *crashHarness) tick(t *testing.T, pods ...*corev1.Pod) {
	t.Helper()
	ctx := context.Background()
	existing, _ := h.cs.CoreV1().Pods(testNS).List(ctx, metav1.ListOptions{})
	for _, p := range existing.Items {
		_ = h.cs.CoreV1().Pods(testNS).Delete(ctx, p.Name, metav1.DeleteOptions{})
	}
	for _, p := range pods {
		if _, err := h.cs.CoreV1().Pods(testNS).Create(ctx, p, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	h.w.checkPods(ctx)
	h.now = h.now.Add(HeartbeatInterval)
}

func TestPodCrashed_ServiceIsShortSlugFromServiceLabel(t *testing.T) {
	h := newCrashHarness(t)
	h.tick(t, svcPod("shop-api-production-abc-1", "CrashLoopBackOff"))
	if len(h.events) != 1 {
		t.Fatalf("got %d events, want 1", len(h.events))
	}
	e := h.events[0]
	if e.Service != "api" {
		t.Errorf("Service = %q, want short slug %q", e.Service, "api")
	}
	if e.URL == "" || !strings.Contains(e.URL, "?service=api") || strings.Contains(e.URL, "service=shop-api") {
		t.Errorf("URL = %q, want ?service=api", e.URL)
	}
	if !strings.Contains(e.Title, "production") {
		t.Errorf("Title = %q, want env name", e.Title)
	}
}

func TestPodEnvName_ReadsEnvCRLabel(t *testing.T) {
	h := newCrashHarness(t)
	p := svcPod("x", "CrashLoopBackOff")
	p.Labels["app.kubernetes.io/instance"] = "shop-api-staging-production"
	p.Labels[kube.LabelService] = "shop-api-staging"
	env := &unstructured.Unstructured{}
	env.SetGroupVersionKind(schema.GroupVersionKind{
		Group: kube.GVREnvironments.Group, Version: kube.GVREnvironments.Version, Kind: "KusoEnvironment",
	})
	env.SetNamespace(testNS)
	env.SetName("shop-api-staging-production")
	env.SetLabels(map[string]string{kube.LabelEnv: "staging"})
	if err := h.w.Kube.Dynamic.(*dynamicfake.FakeDynamicClient).Tracker().Create(kube.GVREnvironments, env, testNS); err != nil {
		t.Fatal(err)
	}
	if got := h.w.podEnvName(context.Background(), p, podServiceShort(p)); got != "staging" {
		t.Errorf("env = %q, want staging", got)
	}
	// No CR: derive from the env CR name.
	p.Labels["app.kubernetes.io/instance"] = "shop-api-pr-7"
	p.Labels[kube.LabelService] = "shop-api"
	if got := h.w.podEnvName(context.Background(), p, podServiceShort(p)); got != "preview-pr-7" {
		t.Errorf("env = %q, want preview-pr-7", got)
	}
}

func TestPodServiceShort_FallbackWithoutServiceLabel(t *testing.T) {
	p := svcPod("x", "CrashLoopBackOff")
	delete(p.Labels, kube.LabelService)
	if got := podServiceShort(p); got != "api" {
		t.Errorf("got %q, want api", got)
	}
}

func TestPodCrashed_ReasonFlipAndReplicasDoNotReAlert(t *testing.T) {
	h := newCrashHarness(t)
	h.tick(t, svcPod("shop-api-production-abc-1", "ErrImagePull"))
	h.tick(t, svcPod("shop-api-production-abc-1", "ImagePullBackOff"))
	h.tick(t, svcPod("shop-api-production-abc-1", "ErrImagePull"),
		svcPod("shop-api-production-abc-2", "ImagePullBackOff"))
	if len(h.events) != 1 {
		t.Fatalf("got %d events, want 1 across reason flips + replicas", len(h.events))
	}
}

func TestPodCrashed_GapWithinCooldownDoesNotReAlert(t *testing.T) {
	h := newCrashHarness(t)
	h.tick(t, svcPod("shop-api-production-abc-1", "CrashLoopBackOff"))
	h.tick(t, svcPod("shop-api-production-abc-1", "")) // Running between restarts
	h.tick(t, svcPod("shop-api-production-abc-1", "CrashLoopBackOff"))
	if len(h.events) != 1 {
		t.Fatalf("got %d events, want 1", len(h.events))
	}
}

func TestPodCrashed_ReAlertsAfterCooldown(t *testing.T) {
	h := newCrashHarness(t)
	h.tick(t, svcPod("shop-api-production-abc-1", "CrashLoopBackOff"))
	h.now = h.now.Add(CrashAlertCooldown)
	h.tick(t, svcPod("shop-api-production-abc-1", "")) // healthy; prunes the key
	h.tick(t, svcPod("shop-api-production-def-1", "CrashLoopBackOff"))
	if len(h.events) != 2 {
		t.Fatalf("got %d events, want 2 (new episode after cooldown)", len(h.events))
	}
}

// healthyTicks is enough consecutive healthy ticks to cover the window.
var healthyTicks = int(RecoveryStableWindow/HeartbeatInterval) + 1

func countType(events []notify.Event, typ notify.EventType) int {
	n := 0
	for _, e := range events {
		if e.Type == typ {
			n++
		}
	}
	return n
}

func TestPodCrashed_SinceIsEpisodeStart(t *testing.T) {
	h := newCrashHarness(t)
	start := h.now
	h.tick(t, svcPod("shop-api-production-abc-1", "CrashLoopBackOff"))
	if len(h.events) != 1 {
		t.Fatalf("got %d events, want 1", len(h.events))
	}
	if !h.w.crashSeen[crashKey(svcPod("shop-api-production-abc-1", ""))].since.Equal(start) {
		t.Errorf("episode since not recorded as %v", start)
	}
	found := false
	for _, f := range h.events[0].Fields {
		if f.Name == "Since" && f.Value == notify.TimeToken(start) {
			found = true
		}
	}
	if !found {
		t.Errorf("Fields = %+v, want Since = %q", h.events[0].Fields, notify.TimeToken(start))
	}
}

func TestPodRecovered_FiresOnceAfterStableWindow(t *testing.T) {
	h := newCrashHarness(t)
	h.tick(t, svcPod("shop-api-production-abc-1", "CrashLoopBackOff"))
	h.tick(t, svcPod("shop-api-production-abc-2", "CrashLoopBackOff"))
	for i := 0; i < healthyTicks-1; i++ {
		h.tick(t, svcPod("shop-api-production-abc-1", ""))
		if n := countType(h.events, notify.EventPodRecovered); n != 0 {
			t.Fatalf("recovery fired after %d healthy ticks, before the window", i+1)
		}
	}
	h.tick(t, svcPod("shop-api-production-abc-1", ""))
	if n := countType(h.events, notify.EventPodRecovered); n != 1 {
		t.Fatalf("got %d recoveries after the window, want 1", n)
	}
	rec := h.events[len(h.events)-1]
	if rec.Service != "api" || rec.Env != "production" {
		t.Errorf("recovery scope = %s/%s, want api/production", rec.Service, rec.Env)
	}
	// Down = first crash → first healthy tick: two crash ticks.
	if !strings.Contains(rec.Description, "2m") {
		t.Errorf("Description = %q, want episode length 2m", rec.Description)
	}
	for i := 0; i < healthyTicks; i++ {
		h.tick(t, svcPod("shop-api-production-abc-1", ""))
	}
	if n := countType(h.events, notify.EventPodRecovered); n != 1 {
		t.Fatalf("got %d recoveries, want exactly 1", n)
	}
}

func TestPodRecovered_FlappingCrashloopNeitherRecoversNorReAlerts(t *testing.T) {
	h := newCrashHarness(t)
	for i := 0; i < 3*healthyTicks; i++ {
		reason := "CrashLoopBackOff"
		if i%2 == 1 {
			reason = "" // briefly Running+Ready between restarts
		}
		h.tick(t, svcPod("shop-api-production-abc-1", reason))
	}
	if n := countType(h.events, notify.EventPodCrashed); n != 1 {
		t.Errorf("got %d crash alerts, want 1", n)
	}
	if n := countType(h.events, notify.EventPodRecovered); n != 0 {
		t.Errorf("got %d recoveries for a flapping crashloop, want 0", n)
	}
}

func TestPodRecovered_RestartBetweenTicksResetsWindow(t *testing.T) {
	h := newCrashHarness(t)
	h.tick(t, svcPod("shop-api-production-abc-1", "CrashLoopBackOff"))
	for i := 0; i < healthyTicks; i++ {
		p := svcPod("shop-api-production-abc-1", "")
		// Crashed and came back between every tick: never seen Waiting.
		p.Status.ContainerStatuses[0].RestartCount = int32(i + 1)
		h.tick(t, p)
	}
	if n := countType(h.events, notify.EventPodRecovered); n != 0 {
		t.Errorf("got %d recoveries while restarts kept climbing, want 0", n)
	}
}

func TestPodRecovered_NotOnPodsVanishing(t *testing.T) {
	h := newCrashHarness(t)
	h.tick(t, svcPod("shop-api-production-abc-1", "CrashLoopBackOff"))
	for i := 0; i < int(CrashAlertCooldown/HeartbeatInterval)+2; i++ {
		h.tick(t) // env deleted / scaled to 0
	}
	if n := countType(h.events, notify.EventPodRecovered); n != 0 {
		t.Errorf("got %d recoveries with no pods, want 0", n)
	}
	if len(h.w.crashSeen) != 0 {
		t.Errorf("episode not expired via cooldown")
	}
}

func TestPodCrashed_AlertsAgainAfterRecovery(t *testing.T) {
	h := newCrashHarness(t)
	h.tick(t, svcPod("shop-api-production-abc-1", "CrashLoopBackOff"))
	for i := 0; i < healthyTicks; i++ {
		h.tick(t, svcPod("shop-api-production-abc-1", ""))
	}
	h.tick(t, svcPod("shop-api-production-abc-1", "CrashLoopBackOff"))
	if n := countType(h.events, notify.EventPodCrashed); n != 2 {
		t.Fatalf("got %d crash alerts, want 2 (new episode after recovery)", n)
	}
	if n := countType(h.events, notify.EventPodRecovered); n != 1 {
		t.Fatalf("got %d recoveries, want 1", n)
	}
}

// Crash detection listed pods only in the home namespace, so projects with
// their own namespace (kuso-<project>) never got a crash alert (live: a
// crashlooping cmp/nocaps pod in kuso-cmp produced no event). Pods owned
// by a Job (crons, runs, builds, seeds) are not long-running workloads —
// they fail through their own events — and were reported as a service
// crash under the cron's name.
func TestCheckPods_AllNamespacesAndSkipsJobPods(t *testing.T) {
	h := newCrashHarness(t)
	ctx := context.Background()

	custom := svcPod("cmp-web-production-x-1", "CrashLoopBackOff")
	custom.Namespace = "kuso-cmp"
	custom.Labels[kube.LabelProject] = "cmp"
	custom.Labels[kube.LabelService] = "cmp-web"
	custom.Labels["app.kubernetes.io/instance"] = "cmp-web-production"

	job := svcPod("shop-api-nightly-2984-abc", "CrashLoopBackOff")
	job.Labels["app.kubernetes.io/instance"] = "shop-api-nightly"
	job.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: "shop-api-nightly-2984", UID: "j"}}

	for _, p := range []*corev1.Pod{custom, job} {
		if _, err := h.cs.CoreV1().Pods(p.Namespace).Create(ctx, p, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	h.w.checkPods(ctx)

	var projects []string
	for _, e := range h.events {
		if e.Type == notify.EventPodCrashed {
			projects = append(projects, e.Project+"/"+e.Service)
		}
	}
	if len(projects) != 1 || projects[0] != "cmp/web" {
		t.Errorf("crash events %v, want exactly [cmp/web] (custom namespace seen, Job pod skipped)", projects)
	}
}
