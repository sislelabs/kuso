package buildkit

import (
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDefaultBuildCap(t *testing.T) {
	cases := []struct {
		name        string
		milli       int64
		daemons, mp int
		want        int
	}{
		{"2-core box floors at 2", 2000, 1, 3, 2},
		{"16 cores, one daemon", 16000, 1, 3, 4},
		{"20 x 8 cores, one daemon: daemon-bound", 160000, 1, 3, 6},
		{"20 x 8 cores, three daemons", 160000, 3, 3, 18},
		{"unknown CPU uses daemon capacity", 0, 1, 3, 6},
		{"zero daemons treated as one", 160000, 0, 0, 6},
	}
	for _, tc := range cases {
		if got := DefaultBuildCap(tc.milli, tc.daemons, tc.mp); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestPickIsStickyAndOnlyMovesTheLeavingNodesProjects(t *testing.T) {
	three := []Daemon{{"a", "10.0.0.1"}, {"b", "10.0.0.2"}, {"c", "10.0.0.3"}}
	projects := make([]string, 200)
	for i := range projects {
		projects[i] = fmt.Sprintf("p%d", i)
	}
	home := map[string]string{}
	perNode := map[string]int{}
	for _, p := range projects {
		d, ok := Pick(p, three)
		if !ok {
			t.Fatal("no daemon picked")
		}
		if again, _ := Pick(p, []Daemon{three[2], three[0], three[1]}); again != d {
			t.Fatalf("%s: pick depends on list order: %v vs %v", p, d, again)
		}
		home[p] = d.Node
		perNode[d.Node]++
	}
	for _, n := range []string{"a", "b", "c"} {
		if perNode[n] < 40 {
			t.Errorf("node %s got %d of 200 projects, want a rough third: %v", n, perNode[n], perNode)
		}
	}
	two := []Daemon{three[0], three[2]} // b leaves
	for _, p := range projects {
		d, _ := Pick(p, two)
		if home[p] != "b" && d.Node != home[p] {
			t.Errorf("%s moved %s -> %s although its node stayed", p, home[p], d.Node)
		}
	}
	restarted := []Daemon{{"a", "10.0.9.9"}, three[1], three[2]}
	for _, p := range projects {
		if d, _ := Pick(p, restarted); d.Node != home[p] {
			t.Errorf("%s moved after a daemon restart changed only its IP", p)
		}
	}
}

func TestHostForFallsBackToTheService(t *testing.T) {
	if got := HostFor("acme", nil); got != DefaultHost {
		t.Fatalf("no daemons: got %q", got)
	}
	if got := HostFor("acme", []Daemon{{"a", "10.42.1.3"}}); got != "tcp://10.42.1.3:1234" {
		t.Fatalf("one daemon: got %q", got)
	}
}

func TestReadyDaemonsSkipsPodsThatCannotBuild(t *testing.T) {
	pod := func(name, node, ip string, phase corev1.PodPhase, ready bool) *corev1.Pod {
		st := corev1.ConditionFalse
		if ready {
			st = corev1.ConditionTrue
		}
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec:       corev1.PodSpec{NodeName: node},
			Status: corev1.PodStatus{Phase: phase, PodIP: ip,
				Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: st}}},
		}
	}
	gone := pod("gone", "d", "10.0.0.4", corev1.PodRunning, true)
	now := metav1.Now()
	gone.DeletionTimestamp = &now
	got := ReadyDaemons([]*corev1.Pod{
		pod("ok-b", "b", "10.0.0.2", corev1.PodRunning, true),
		pod("ok-a", "a", "10.0.0.1", corev1.PodRunning, true),
		pod("unready", "c", "10.0.0.3", corev1.PodRunning, false),
		pod("pending", "e", "", corev1.PodPending, false),
		pod("dup-a", "a", "10.0.0.9", corev1.PodRunning, true),
		gone,
	})
	want := []Daemon{{"a", "10.0.0.1"}, {"b", "10.0.0.2"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("ReadyDaemons = %v, want %v", got, want)
	}
}
