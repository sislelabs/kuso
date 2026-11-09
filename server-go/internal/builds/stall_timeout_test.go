package builds

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/failures"
	"kuso/server/internal/kube"
)

// builderPullPod is the sandbox case: cache-init and clone finished, the
// env-detect init container can't pull its image.
func builderPullPod(name string, created time.Time) *corev1.Pod {
	done := corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Reason: "Completed"}}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name + "-abcde", Namespace: "kuso",
			CreationTimestamp: metav1.NewTime(created),
			Labels:            map[string]string{"app.kubernetes.io/instance": name},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			InitContainerStatuses: []corev1.ContainerStatus{
				{Name: "cache-init", State: done},
				{Name: "clone", State: done},
				{Name: "env-detect", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
					Reason: "ImagePullBackOff", Message: `Back-off pulling image "ghcr.io/sislelabs/kuso-env-detect:v2"`,
				}}},
			},
		},
	}
}

func seedStalledBuild(t *testing.T, name string, age time.Duration, pod *corev1.Pod) (*Service, *kube.KusoBuild) {
	t.Helper()
	at := time.Now().Add(-age)
	b := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "kuso",
			CreationTimestamp: metav1.NewTime(at),
			Annotations: map[string]string{
				annPhase:        "running",
				annDispatchedAt: at.UTC().Format(time.RFC3339),
			},
		},
		Spec: kube.KusoBuildSpec{Project: "alpha", Service: "alpha-api", Branch: "main"},
	}
	s := fakeService(t, seedBuild(b))
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso"}, Status: batchv1.JobStatus{Active: 1}}
	if _, err := s.Kube.Clientset.BatchV1().Jobs("kuso").Create(context.Background(), job, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if pod != nil {
		if _, err := s.Kube.Clientset.CoreV1().Pods("kuso").Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	return s, b
}

func TestCheckBuild_FailsBuildStuckPullingBuilderImage(t *testing.T) {
	old := startTimeoutJobGrace
	startTimeoutJobGrace = 0
	t.Cleanup(func() { startTimeoutJobGrace = old })

	name := "alpha-api-stuck"
	s, b := seedStalledBuild(t, name, 12*time.Minute, builderPullPod(name, time.Now().Add(-12*time.Minute)))
	p := &Poller{Svc: s, Logger: slog.Default()}
	if err := p.checkBuild(context.Background(), "kuso", b); err != nil {
		t.Fatalf("checkBuild: %v", err)
	}
	got, err := s.Kube.GetKusoBuild(context.Background(), "kuso", name)
	if err != nil {
		t.Fatal(err)
	}
	if ph := buildPhase(got); ph != "failed" {
		t.Fatalf("phase = %q, want failed", ph)
	}
	msg := got.Annotations[annMessage]
	if !strings.HasPrefix(msg, PlatformFailurePrefix+": builder image unavailable") || !strings.HasSuffix(msg, "(env-detect)") {
		t.Errorf("message = %q, want the platform prefix and the stuck container", msg)
	}
	var c failures.Classification
	if err := json.Unmarshal([]byte(got.Annotations[annClassification]), &c); err != nil || c.Kind != KindBuildPlatform {
		t.Errorf("classification = %s (%v), want kind %s", got.Annotations[annClassification], err, KindBuildPlatform)
	}
	if !isPlatformFailure(got) {
		t.Error("isPlatformFailure = false on the failed build")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, err := s.Kube.Clientset.BatchV1().Jobs("kuso").Get(context.Background(), name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the stuck build's Job was not deleted")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCheckBuild_LeavesBuildsInsideTheStartTimeout(t *testing.T) {
	t.Run("pulling for 5 minutes", func(t *testing.T) {
		name := "alpha-api-young"
		s, b := seedStalledBuild(t, name, 5*time.Minute, builderPullPod(name, time.Now().Add(-5*time.Minute)))
		p := &Poller{Svc: s, Logger: slog.Default()}
		if err := p.checkBuild(context.Background(), "kuso", b); err != nil {
			t.Fatalf("checkBuild: %v", err)
		}
		got, _ := s.Kube.GetKusoBuild(context.Background(), "kuso", name)
		if ph := buildPhase(got); ph == "failed" {
			t.Fatalf("a 5-minute pull stall was failed")
		}
	})
	t.Run("a long build that is running", func(t *testing.T) {
		name := "alpha-api-long"
		pod := builderPullPod(name, time.Now().Add(-40*time.Minute))
		pod.Status.Phase = corev1.PodRunning
		s, b := seedStalledBuild(t, name, 40*time.Minute, pod)
		p := &Poller{Svc: s, Logger: slog.Default()}
		if err := p.checkBuild(context.Background(), "kuso", b); err != nil {
			t.Fatalf("checkBuild: %v", err)
		}
		got, _ := s.Kube.GetKusoBuild(context.Background(), "kuso", name)
		if ph := buildPhase(got); ph == "failed" {
			t.Fatalf("a running build was failed")
		}
	})
}

// The live view marks the step a stall is in from Container: clone had
// finished, so it must name env-detect, not the first init container.
func TestStallFor_NamesTheStuckContainer(t *testing.T) {
	now := time.Now()
	st := StallFor([]corev1.Pod{*builderPullPod("b", now.Add(-2*time.Minute))}, now.Add(-2*time.Minute), now)
	if st == nil || st.Container != "env-detect" {
		t.Fatalf("stall = %+v, want container env-detect", st)
	}
}

func TestStallExpired(t *testing.T) {
	now := time.Now()
	limit := 10 * time.Minute
	cases := []struct {
		kind string
		age  time.Duration
		want bool
	}{
		{StallImagePull, 11 * time.Minute, true},
		{StallImagePull, 9 * time.Minute, false},
		{StallConfig, 11 * time.Minute, true},
		// Waiting for a node or a big first pull gets twice as long.
		{StallUnschedulable, 11 * time.Minute, false},
		{StallUnschedulable, 21 * time.Minute, true},
		{StallStarting, 15 * time.Minute, false},
	}
	for _, c := range cases {
		if got := stallExpired(&Stall{Kind: c.kind, Since: now.Add(-c.age)}, now, limit); got != c.want {
			t.Errorf("%s after %s: expired = %v, want %v", c.kind, c.age, got, c.want)
		}
	}
	if stallExpired(nil, now, limit) {
		t.Error("nil stall expired")
	}
}
