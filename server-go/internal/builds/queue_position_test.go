package builds

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// queuedBuild seeds a KusoBuild carrying the build-state=queued label
// the dispatcher (and QueuePositions) selects on.
func queuedBuild(name, project, service string, created time.Time) seed {
	return seedBuild(&kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "kuso",
			CreationTimestamp: metav1.NewTime(created),
			Labels: map[string]string{
				"kuso.sislelabs.com/project":     project,
				"kuso.sislelabs.com/service":     service,
				"kuso.sislelabs.com/build-state": "queued",
			},
		},
		Spec: kube.KusoBuildSpec{Project: project, Service: service, Branch: "main"},
	})
}

func TestQueuePositions_OrdersByCreationAcrossProjects(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	running := seedBuild(&kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "alpha-web-running0000",
			Namespace:         "kuso",
			CreationTimestamp: metav1.NewTime(base.Add(-5 * time.Minute)),
			Labels: map[string]string{
				"kuso.sislelabs.com/project": "alpha",
				"kuso.sislelabs.com/service": "alpha-web",
				// no build-state label — in-flight, not queued
			},
		},
		Spec: kube.KusoBuildSpec{Project: "alpha", Service: "alpha-web", Branch: "main"},
	})
	s := fakeService(t,
		queuedBuild("beta-api-cccccccccccc", "beta", "beta-api", base.Add(2*time.Minute)),
		queuedBuild("alpha-web-aaaaaaaaaaaa", "alpha", "alpha-web", base),
		queuedBuild("alpha-web-bbbbbbbbbbbb", "alpha", "alpha-web", base.Add(1*time.Minute)),
		running,
	)

	got := s.QueuePositions(context.Background())
	want := map[string]int{
		"alpha-web-aaaaaaaaaaaa": 1,
		"alpha-web-bbbbbbbbbbbb": 2,
		"beta-api-cccccccccccc":  3,
	}
	if len(got) != len(want) {
		t.Fatalf("positions: got %v, want %v", got, want)
	}
	for name, pos := range want {
		if got[name] != pos {
			t.Errorf("position[%s]: got %d, want %d", name, got[name], pos)
		}
	}
	if _, ok := got["alpha-web-running0000"]; ok {
		t.Errorf("running build must not appear in queue positions, got %v", got)
	}
}

func TestQueuePositions_TiebreakByName(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	s := fakeService(t,
		queuedBuild("alpha-web-zzz", "alpha", "alpha-web", base),
		queuedBuild("alpha-web-aaa", "alpha", "alpha-web", base),
	)
	got := s.QueuePositions(context.Background())
	if got["alpha-web-aaa"] != 1 || got["alpha-web-zzz"] != 2 {
		t.Errorf("same-timestamp tiebreak by name: got %v", got)
	}
}

func TestQueuePositions_EmptyQueue(t *testing.T) {
	t.Parallel()
	s := fakeService(t)
	if got := s.QueuePositions(context.Background()); len(got) != 0 {
		t.Errorf("empty cluster: got %v, want empty map", got)
	}
}
