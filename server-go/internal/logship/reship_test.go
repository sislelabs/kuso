package logship

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

type fakeLine struct {
	ts   time.Time
	text string
}

// fakeLogs mimics the kubelet: SinceTime is inclusive and travels at
// second precision (metav1.Time query encoding), TailLines keeps the
// last N, and every stream ends at the current end of the log.
type fakeLogs struct {
	mu    sync.Mutex
	lines map[string][]fakeLine // pod/container → log
	opens int
}

func (f *fakeLogs) add(pod, container string, ts time.Time, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := pod + "/" + container
	f.lines[k] = append(f.lines[k], fakeLine{ts, text})
}

func (f *fakeLogs) open(_ context.Context, _, pod string, o *corev1.PodLogOptions) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opens++
	src := f.lines[pod+"/"+o.Container]
	var out []fakeLine
	for _, l := range src {
		if o.SinceTime != nil && l.ts.Before(o.SinceTime.Time.Truncate(time.Second)) {
			continue
		}
		out = append(out, l)
	}
	if o.TailLines != nil && int64(len(out)) > *o.TailLines {
		out = out[int64(len(out))-*o.TailLines:]
	}
	var sb strings.Builder
	for _, l := range out {
		if o.Timestamps {
			sb.WriteString(l.ts.UTC().Format(time.RFC3339Nano) + " ")
		}
		sb.WriteString(l.text + "\n")
	}
	return io.NopCloser(strings.NewReader(sb.String())), nil
}

func testPod(name string, phase corev1.PodPhase, terminated bool) *corev1.Pod {
	cs := corev1.ContainerStatus{Name: "main"}
	if terminated {
		cs.State.Terminated = &corev1.ContainerStateTerminated{ExitCode: 0}
	} else {
		cs.State.Running = &corev1.ContainerStateRunning{}
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "kuso", UID: types.UID("uid-" + name),
			Labels: map[string]string{kube.LabelProject: "p", "kuso.sislelabs.com/service": "svc"},
		},
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "main"}}},
		Status: corev1.PodStatus{Phase: phase, ContainerStatuses: []corev1.ContainerStatus{cs}},
	}
}

func newTestShipper(logs *fakeLogs, pods ...*corev1.Pod) (*Shipper, *fake.Clientset) {
	objs := make([]runtime.Object, 0, len(pods))
	for _, p := range pods {
		objs = append(objs, p)
	}
	cs := fake.NewSimpleClientset(objs...)
	s := New(nil, &kube.Client{Clientset: cs}, "kuso", slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.openLogs = logs.open
	s.lastStoredTs = func(context.Context, string, string, string, time.Time) (time.Time, error) {
		return time.Time{}, nil
	}
	return s, cs
}

// tick runs one reconcile and waits for every stream it opened to end.
func tick(t *testing.T, s *Shipper) {
	t.Helper()
	s.reconcilePods(context.Background())
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		n := 0
		for _, st := range s.containers {
			if st.streaming {
				n++
			}
		}
		s.mu.Unlock()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("streams did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func shipped(s *Shipper) map[string]int {
	s.bufMu.Lock()
	defer s.bufMu.Unlock()
	out := map[string]int{}
	for _, l := range s.buf {
		out[l.Pod+": "+l.Line]++
	}
	return out
}

func assertExactlyOnce(t *testing.T, got map[string]int, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("shipped %d distinct lines, want %d: %v", len(got), len(want), got)
	}
	for _, w := range want {
		if got[w] != 1 {
			t.Fatalf("line %q shipped %d times, want 1 (all: %v)", w, got[w], got)
		}
	}
}

func TestTerminatedPodShippedExactlyOnceAcrossTicks(t *testing.T) {
	base := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	logs := &fakeLogs{lines: map[string][]fakeLine{}}
	for i := 0; i < 3; i++ {
		logs.add("backup", "main", base.Add(time.Duration(i)*time.Millisecond), fmt.Sprintf("line %d", i))
	}
	s, _ := newTestShipper(logs, testPod("backup", corev1.PodSucceeded, true))

	for i := 0; i < 5; i++ {
		tick(t, s)
	}
	assertExactlyOnce(t, shipped(s), "backup: line 0", "backup: line 1", "backup: line 2")
	if logs.opens != 1 {
		t.Fatalf("finished container reopened: %d opens, want 1", logs.opens)
	}
}

func TestRunningPodResumesWithoutDuplicates(t *testing.T) {
	base := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	logs := &fakeLogs{lines: map[string][]fakeLine{}}
	logs.add("web", "main", base.Add(100*time.Millisecond), "a")
	logs.add("web", "main", base.Add(200*time.Millisecond), "b")
	s, _ := newTestShipper(logs, testPod("web", corev1.PodRunning, false))

	tick(t, s)
	// Same wall-clock second as "b": SinceTime's second precision
	// re-serves a and b, which must be deduped.
	logs.add("web", "main", base.Add(300*time.Millisecond), "c")
	logs.add("web", "main", base.Add(2*time.Second), "d")
	tick(t, s)
	tick(t, s)

	assertExactlyOnce(t, shipped(s), "web: a", "web: b", "web: c", "web: d")
}

func TestPodFinishedBetweenTicksIsShipped(t *testing.T) {
	base := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	logs := &fakeLogs{lines: map[string][]fakeLine{}}
	s, cs := newTestShipper(logs)
	tick(t, s)

	logs.add("job", "main", base, "boom")
	p := testPod("job", corev1.PodFailed, true)
	p.Status.ContainerStatuses[0].State.Terminated.ExitCode = 1
	if _, err := cs.CoreV1().Pods("kuso").Create(context.Background(), p, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	tick(t, s)
	tick(t, s)

	assertExactlyOnce(t, shipped(s), "job: boom")
}

func TestStateEvictedForVanishedPods(t *testing.T) {
	logs := &fakeLogs{lines: map[string][]fakeLine{}}
	logs.add("gone", "main", time.Now(), "x")
	s, cs := newTestShipper(logs, testPod("gone", corev1.PodSucceeded, true))
	tick(t, s)
	if err := cs.CoreV1().Pods("kuso").Delete(context.Background(), "gone", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	tick(t, s)
	s.mu.Lock()
	n := len(s.containers)
	s.mu.Unlock()
	if n != 0 {
		t.Fatalf("state kept for %d vanished containers", n)
	}
}

func TestRunningPodBurstBetweenTicksIsNotTruncated(t *testing.T) {
	base := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	logs := &fakeLogs{lines: map[string][]fakeLine{}}
	logs.add("web", "main", base, "start")
	s, _ := newTestShipper(logs, testPod("web", corev1.PodRunning, false))
	tick(t, s)

	want := []string{"web: start"}
	for i := 0; i < 3*firstTailLines; i++ {
		logs.add("web", "main", base.Add(time.Duration(i+1)*time.Millisecond), fmt.Sprintf("burst %d", i))
		want = append(want, fmt.Sprintf("web: burst %d", i))
	}
	tick(t, s)

	assertExactlyOnce(t, shipped(s), want...)
}
