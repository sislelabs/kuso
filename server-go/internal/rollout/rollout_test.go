package rollout

import (
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

func rsPod(name, hash string, age time.Duration, ready bool, restarts int32, now time.Time) *corev1.Pod {
	st := corev1.ConditionFalse
	if ready {
		st = corev1.ConditionTrue
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Labels:            map[string]string{"pod-template-hash": hash},
			CreationTimestamp: metav1.NewTime(now.Add(-age)),
		},
		Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: st}},
			ContainerStatuses: []corev1.ContainerStatus{{Name: "app", Ready: ready, RestartCount: restarts}},
		},
	}
}

func TestAnalyzeRollout(t *testing.T) {
	t.Parallel()
	now := time.Now()
	hc := &kube.KusoHealthcheck{Path: "/health"}
	old := rsPod("old", "aaa", time.Hour, true, 0, now)

	cases := []struct {
		name        string
		pods        []*corev1.Pod
		hc          *kube.KusoHealthcheck
		deadline    bool
		wantFlight  bool
		wantFailing bool
		wantKind    string
	}{
		{"settled: one ReplicaSet", []*corev1.Pod{old}, hc, false, false, false, ""},
		{"healthy rollout, new pod just started", []*corev1.Pod{old, rsPod("new", "bbb", 10*time.Second, false, 0, now)}, hc, false, true, false, ""},
		{"new pod restarted by the liveness probe", []*corev1.Pod{old, rsPod("new", "bbb", 40*time.Second, false, 1, now)}, hc, false, true, true, KindHealthcheck},
		{"new pod never passes readiness past the grace", []*corev1.Pod{old, rsPod("new", "bbb", 2*time.Minute, false, 0, now)}, hc, false, true, true, KindHealthcheck},
		{"no health check: a young not-ready pod is just starting", []*corev1.Pod{old, rsPod("new", "bbb", 2*time.Minute, false, 0, now)}, nil, false, true, false, ""},
		{"no health check: deadline exceeded", []*corev1.Pod{old, rsPod("new", "bbb", 11*time.Minute, false, 0, now)}, nil, true, true, true, KindStalled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := Analyze(tc.pods, tc.hc, tc.deadline, now)
			if f.InFlight != tc.wantFlight || f.Failing != tc.wantFailing || f.Kind != tc.wantKind {
				t.Fatalf("got inFlight=%v failing=%v kind=%q, want %v %v %q", f.InFlight, f.Failing, f.Kind, tc.wantFlight, tc.wantFailing, tc.wantKind)
			}
			if tc.wantFlight && (!f.PreviousServing || !f.PreviousSince.Equal(old.CreationTimestamp.Time)) {
				t.Fatalf("old ready pod must count as previous serving")
			}
		})
	}

	t.Run("crashloop without a health check", func(t *testing.T) {
		p := rsPod("new", "bbb", time.Minute, false, 4, now)
		p.Status.ContainerStatuses[0].State.Waiting = &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}
		p.Status.ContainerStatuses[0].LastTerminationState.Terminated = &corev1.ContainerStateTerminated{ExitCode: 2}
		f := Analyze([]*corev1.Pod{old, p}, nil, false, now)
		if !f.Failing || f.Kind != KindCrash || !strings.Contains(f.Cause, "exit code 2") {
			t.Fatalf("got %+v", f)
		}
	})
}

func TestProbeOutcome(t *testing.T) {
	t.Parallel()
	for msg, want := range map[string]string{
		"Readiness probe failed: HTTP probe failed with statuscode: 404":                             "404",
		`Liveness probe failed: Get "http://10.0.0.1:8080/x": dial tcp: connect: connection refused`: "connection refused",
		"Readiness probe failed: Get ...: context deadline exceeded":                                 "timed out",
		"something else": "",
	} {
		if got := ProbeOutcome(msg); got != want {
			t.Errorf("ProbeOutcome(%q) = %q, want %q", msg, got, want)
		}
	}
}

func TestRolloutCompletedAt(t *testing.T) {
	t.Parallel()
	done := metav1.NewTime(time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC))
	dep := &appsv1.Deployment{Status: appsv1.DeploymentStatus{Conditions: []appsv1.DeploymentCondition{
		{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue, LastUpdateTime: metav1.Now()},
		{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionTrue, Reason: "NewReplicaSetAvailable", LastUpdateTime: done},
	}}}
	if got := CompletedAt(dep); !got.Equal(done.Time) {
		t.Fatalf("got %v, want %v", got, done.Time)
	}
	dep.Status.Conditions[1].Reason = "ReplicaSetUpdated"
	if got := CompletedAt(dep); !got.IsZero() {
		t.Fatalf("in-flight rollout must have no completion, got %v", got)
	}
}

func TestPullCause(t *testing.T) {
	t.Parallel()
	for msg, want := range map[string]string{
		`ErrImagePull: failed to pull and unpack image "docker.io/traefik/whoami:v9.9.9-nope": failed to resolve reference "docker.io/traefik/whoami:v9.9.9-nope": docker.io/traefik/whoami:v9.9.9-nope: not found`: PullNotFound,
		`Failed to pull image "ghcr.io/acme/app:v2": failed to authorize: failed to fetch anonymous token: unexpected status from GET request: 401 Unauthorized`:                                                    PullUnauthorized,
		`Failed to pull image "ghcr.io/acme/app:v2": 403 Forbidden`: PullUnauthorized,
		`Failed to pull image "acme/private": pull access denied, repository does not exist or may require authorization: server message: insufficient_scope: authorization failed`: PullUnauthorized,
		`Failed to pull image "reg.local/app:v1": dial tcp: lookup reg.local: no such host`:                                                                                         PullUnreachable,
		// A digest is hex; its "401" run isn't a status code.
		`Failed to pull image "r/app@sha256:ab4013cd": manifest unknown`:                  PullNotFound,
		`ImagePullBackOff: Back-off pulling image "docker.io/traefik/whoami:v9.9.9-nope"`: "",
	} {
		if got := PullCause(msg); got != want {
			t.Errorf("PullCause(%q) = %q, want %q", msg, got, want)
		}
	}
}

func TestImageTag(t *testing.T) {
	t.Parallel()
	for ref, want := range map[string]string{
		"traefik/whoami:v1.11.0":                  "v1.11.0",
		"localhost:5000/demo/api":                 "",
		"localhost:5000/demo/api:c850e9b0ef35-mw": "c850e9b0ef35-mw",
		"r/app@sha256:0123456789abcdef0123":       "0123456789ab",
	} {
		if got := ImageTag(ref); got != want {
			t.Errorf("ImageTag(%q) = %q, want %q", ref, got, want)
		}
	}
}

func TestPendingApply(t *testing.T) {
	t.Parallel()
	dep := &appsv1.Deployment{}
	dep.Spec.Template.Spec.Containers = []corev1.Container{{Name: "sidecar", Image: "busybox"}, {Name: "app", Image: "traefik/whoami:v1.11.0"}}
	if cur, ok := PendingApply(dep, "traefik/whoami:v9.9.9-nope"); !ok || cur != "traefik/whoami:v1.11.0" {
		t.Fatalf("spec ahead of the template: got %q %v", cur, ok)
	}
	if _, ok := PendingApply(dep, "traefik/whoami:v1.11.0"); ok {
		t.Fatal("an applied image must not be pending")
	}
	if _, ok := PendingApply(nil, "x:y"); ok {
		t.Fatal("no Deployment known: nothing to compare")
	}
}

func TestAnalyzeImages(t *testing.T) {
	t.Parallel()
	now := time.Now()
	old := rsPod("old", "aaa", time.Hour, true, 0, now)
	old.Spec.Containers = []corev1.Container{{Name: "app", Image: "traefik/whoami:v1.11.0"}}
	bad := rsPod("new", "bbb", 20*time.Second, false, 0, now)
	bad.Spec.Containers = []corev1.Container{{Name: "app", Image: "traefik/whoami:v9.9.9-nope"}}
	bad.Status.ContainerStatuses[0].State.Waiting = &corev1.ContainerStateWaiting{
		Reason:  "ErrImagePull",
		Message: `failed to resolve reference "docker.io/traefik/whoami:v9.9.9-nope": not found`,
	}
	f := Analyze([]*corev1.Pod{old, bad}, nil, false, now)
	if !f.Failing || f.Kind != KindImagePull || f.PullCause != PullNotFound {
		t.Fatalf("got %+v", f)
	}
	if f.NewImage != "traefik/whoami:v9.9.9-nope" || f.PreviousImage != "traefik/whoami:v1.11.0" {
		t.Fatalf("images: new %q previous %q", f.NewImage, f.PreviousImage)
	}
	if !strings.Contains(f.Cause, "tag v9.9.9-nope wasn't found") {
		t.Fatalf("cause %q", f.Cause)
	}

	// Back-off carries no registry answer: the cause stays generic.
	bad.Status.ContainerStatuses[0].State.Waiting = &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: `Back-off pulling image "x"`}
	if f := Analyze([]*corev1.Pod{old, bad}, nil, false, now); f.PullCause != "" || f.Cause != "the new version's image can't be pulled" {
		t.Fatalf("back-off: got %+v", f)
	}
}
