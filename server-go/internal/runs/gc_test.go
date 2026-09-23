package runs

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

var gcNow = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

func gcRun(service string, i int, phase string, age time.Duration) *kube.KusoRun {
	return &kube.KusoRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:              fmt.Sprintf("alpha-%s-run-%03d", service, i),
			Namespace:         "kuso",
			CreationTimestamp: metav1.NewTime(gcNow.Add(-age)),
			Annotations:       map[string]string{annRunPhase: phase},
		},
		Spec: kube.KusoRunSpec{Project: "alpha", Service: "alpha-" + service},
	}
}

func remainingRuns(t *testing.T, s *Service) map[string]bool {
	t.Helper()
	l, err := s.Kube.ListKusoRuns(context.Background(), "kuso")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, r := range l {
		out[r.Name] = true
	}
	return out
}

func TestSweepFinishedRuns_KeepsNewestPerServiceAndRecent(t *testing.T) {
	t.Parallel()
	day := 24 * time.Hour
	var seeds []*kube.KusoRun
	// web: 25 finished runs, run 0 is the newest (10 days old), each
	// older one a day further back. Only runs 20..24 are past keep=20.
	for i := 0; i < 25; i++ {
		phase := "succeeded"
		if i%3 == 0 {
			phase = "failed"
		}
		seeds = append(seeds, gcRun("web", i, phase, 10*day+time.Duration(i)*day))
	}
	// worker: 25 runs all 2 days old — past keep but inside retention.
	for i := 0; i < 25; i++ {
		seeds = append(seeds, gcRun("worker", i, "succeeded", 2*day+time.Duration(i)*time.Minute))
	}
	// Ancient runs that never finished must survive regardless of age.
	seeds = append(seeds,
		gcRun("web", 900, "running", 90*day),
		gcRun("web", 901, "pending", 90*day),
		gcRun("web", 902, "", 90*day),
	)
	s := runFakeService(t, seeds...)
	p := &Poller{Svc: s}

	n := p.sweepFinishedRuns(context.Background(), gcNow)

	left := remainingRuns(t, s)
	var gone []string
	for _, r := range seeds {
		if !left[r.Name] {
			gone = append(gone, r.Name)
		}
	}
	sort.Strings(gone)
	want := []string{
		"alpha-web-run-020", "alpha-web-run-021", "alpha-web-run-022",
		"alpha-web-run-023", "alpha-web-run-024",
	}
	if fmt.Sprint(gone) != fmt.Sprint(want) || n != len(want) {
		t.Errorf("deleted %d: %v\nwant %v", n, gone, want)
	}
}

func TestSweepFinishedRuns_BatchesOldestFirst(t *testing.T) {
	t.Parallel()
	day := 24 * time.Hour
	var seeds []*kube.KusoRun
	// 20 recent keepers, then 60 expired runs, run 079 the oldest.
	for i := 0; i < 80; i++ {
		seeds = append(seeds, gcRun("web", i, "succeeded", 8*day+time.Duration(i)*time.Hour))
	}
	s := runFakeService(t, seeds...)
	p := &Poller{Svc: s}

	if n := p.sweepFinishedRuns(context.Background(), gcNow); n != runGCBatch {
		t.Fatalf("first pass deleted %d, want the batch size %d", n, runGCBatch)
	}
	left := remainingRuns(t, s)
	for i := 0; i < 80; i++ {
		name := fmt.Sprintf("alpha-web-run-%03d", i)
		wantGone := i >= 80-runGCBatch
		if left[name] == wantGone {
			t.Errorf("%s: present=%v, want present=%v", name, left[name], !wantGone)
		}
	}
	total := runGCBatch
	for pass := 0; pass < 10; pass++ {
		total += p.sweepFinishedRuns(context.Background(), gcNow)
	}
	if total != 60 || len(remainingRuns(t, s)) != 20 {
		t.Errorf("after draining: deleted %d, left %d; want 60 and 20", total, len(remainingRuns(t, s)))
	}
}
