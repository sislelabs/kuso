package builds

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

type signalArchiver struct {
	logs chan string
}

func (s *signalArchiver) SaveBuildLog(_ context.Context, name, _, _, _, _ string) error {
	s.logs <- name
	return nil
}
func (s *signalArchiver) SaveBuildRecord(context.Context, BuildArchiveRecord) error { return nil }

func archiveBuild(name string) *kube.KusoBuild {
	return &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso", Annotations: map[string]string{annMessage: "generic"}},
		Spec:       kube.KusoBuildSpec{Project: "p", Service: "p-web"},
	}
}

func waitLog(t *testing.T, ch chan string, want string) {
	t.Helper()
	select {
	case got := <-ch:
		if got != want {
			t.Fatalf("archived %q, want %q", got, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("archive task for %q never ran", want)
	}
}

// B-05: the first queueArchive caller's ctx (markSucceeded's 30-min
// promote ctx) must not bound the worker pool's lifetime.
func TestQueueArchive_WorkersOutliveFirstCallerCtx(t *testing.T) {
	arch := &signalArchiver{logs: make(chan string, 4)}
	p := &Poller{Svc: fakeService(t), LogArchive: arch}

	ctx1, cancel := context.WithCancel(context.Background())
	p.queueArchive(ctx1, "kuso", archiveBuild("b1"), "succeeded")
	waitLog(t, arch.logs, "b1")
	cancel()
	time.Sleep(50 * time.Millisecond)

	p.queueArchive(context.Background(), "kuso", archiveBuild("b2"), "succeeded")
	waitLog(t, arch.logs, "b2")
}

// B-06: the worker refines annMessage on the build it archives; that
// must not be the caller's object, which markFailed keeps reading.
func TestQueueArchive_WorkerDoesNotMutateCallerBuild(t *testing.T) {
	svc := fakeService(t)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "b1-pod", Namespace: "kuso",
			Labels: map[string]string{"app.kubernetes.io/instance": "b1"}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:  "build",
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}},
		}}},
	}
	if _, err := svc.Kube.Clientset.CoreV1().Pods("kuso").Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	arch := &signalArchiver{logs: make(chan string, 4)}
	p := &Poller{Svc: svc, LogArchive: arch}
	p.startArchiveWorkers(context.Background())

	b := archiveBuild("b1")
	p.queueArchive(context.Background(), "kuso", b, "failed")
	waitLog(t, arch.logs, "b1")
	// The refinement write happens after SaveBuildLog; give it a moment.
	time.Sleep(200 * time.Millisecond)
	if got := b.Annotations[annMessage]; got != "generic" {
		t.Fatalf("caller's build annotations mutated by archive worker: annMessage=%q", got)
	}
}
