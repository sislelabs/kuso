package projects

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// TestDeriveEnvState pins the full state machine: every state is
// reachable and the precedence order documented on deriveEnvState is
// enforced by the "outranks" cases. If a change reorders precedence,
// this test is the contract that must be consciously updated.
func TestDeriveEnvState(t *testing.T) {
	t.Parallel()
	// healthy is the baseline "all green" input; cases override fields.
	healthy := envStateInput{
		HasImage:       true,
		HasReplicaInfo: true,
		Desired:        2,
		Ready:          2,
		Updated:        2,
	}
	mod := func(f func(*envStateInput)) envStateInput {
		in := healthy
		f(&in)
		return in
	}

	cases := []struct {
		name         string
		in           envStateInput
		wantState    string
		detailSubstr string // "" = don't care
	}{
		// ---- each state reachable -------------------------------------
		{"running", healthy, stateRunning, "2/2"},
		{"degraded partial ready", mod(func(in *envStateInput) { in.Ready = 1 }), stateDegraded, "1/2"},
		{"crashlooping", mod(func(in *envStateInput) {
			in.CrashLooping = true
			in.CrashDetail = `container "app" in CrashLoopBackOff (7 restarts)`
		}), stateCrashLooping, "CrashLoopBackOff"},
		{"deploying zero ready", mod(func(in *envStateInput) { in.Ready = 0 }), stateDeploying, "rollout in progress"},
		{"deploying rollout in flight", mod(func(in *envStateInput) { in.Updated = 1; in.Ready = 2 }), stateDeploying, "1/2 replicas updated"},
		{"building", mod(func(in *envStateInput) { in.BuildPhase = "running" }), stateBuilding, "still serving"},
		{"building queued nothing serving", mod(func(in *envStateInput) { in.BuildPhase = "queued"; in.Ready = 0 }), stateBuilding, "build queued"},
		{"build_failed nothing running", mod(func(in *envStateInput) { in.BuildPhase = "failed"; in.Ready = 0 }), stateBuildFailed, "nothing is running"},
		{"release_failed last green serving", mod(func(in *envStateInput) { in.BuildPhase = "release-failed" }), stateReleaseFailed, "last green build still serving"},
		{"release_failed nothing ready", mod(func(in *envStateInput) { in.BuildPhase = "release-failed"; in.Ready = 0 }), stateReleaseFailed, "no replicas are ready"},
		{"sleeping", mod(func(in *envStateInput) { in.SleepMarked = true; in.Desired = 0; in.Ready = 0 }), stateSleeping, "wakes on the next request"},
		{"stopped hard stop", mod(func(in *envStateInput) { in.Stopped = true }), stateStopped, "kuso stop"},
		{"stopped scaled to zero", mod(func(in *envStateInput) { in.Desired = 0; in.Ready = 0 }), stateStopped, "scaled to 0"},
		{"no_image", envStateInput{}, stateNoImage, "no image built yet"},
		{"unknown no deployment info", envStateInput{HasImage: true}, stateUnknown, ""},

		// ---- last-green semantics -------------------------------------
		// Latest build failed but last-green replicas serve → state stays
		// running with the lastBuildFailed note in the detail.
		{"build failed but last green serving stays running",
			mod(func(in *envStateInput) { in.BuildPhase = "failed" }),
			stateRunning, "latest build failed"},
		{"build failed with partial replicas is degraded with note",
			mod(func(in *envStateInput) { in.BuildPhase = "failed"; in.Ready = 1 }),
			stateDegraded, "latest build failed"},
		// Never-deployed env with a failed build can't be running.
		{"build failed and never deployed",
			envStateInput{BuildPhase: "failed"},
			stateBuildFailed, "no image has ever deployed"},
		{"release failed and never deployed",
			envStateInput{BuildPhase: "release-failed"},
			stateReleaseFailed, "no image has ever deployed"},
		// Cancelled / unknown phases carry no state signal.
		{"cancelled build is no signal", mod(func(in *envStateInput) { in.BuildPhase = "cancelled" }), stateRunning, ""},

		// ---- precedence pins ------------------------------------------
		// 1. stopped outranks an active build AND a crashloop.
		{"stopped outranks building", mod(func(in *envStateInput) {
			in.Stopped = true
			in.BuildPhase = "running"
			in.CrashLooping = true
		}), stateStopped, ""},
		// 2. building outranks crashloop (the push may be the fix).
		{"building outranks crashlooping", mod(func(in *envStateInput) {
			in.BuildPhase = "running"
			in.CrashLooping = true
		}), stateBuilding, ""},
		// 2. building outranks a previous failure's runtime fallout.
		{"building outranks zero ready", mod(func(in *envStateInput) {
			in.BuildPhase = "pending"
			in.Ready = 0
		}), stateBuilding, ""},
		// 4. sleeping outranks the deliberate-scale-to-zero read.
		{"sleep marker wins over bare desired=0", mod(func(in *envStateInput) {
			in.SleepMarked = true
			in.Desired = 0
			in.Ready = 0
		}), stateSleeping, ""},
		// 5. crashloop outranks degraded/deploying arithmetic.
		{"crashloop outranks degraded", mod(func(in *envStateInput) {
			in.CrashLooping = true
			in.Ready = 1
		}), stateCrashLooping, ""},
		{"crashloop outranks deploying", mod(func(in *envStateInput) {
			in.CrashLooping = true
			in.Ready = 0
		}), stateCrashLooping, ""},
		// 6. release_failed outranks runtime states even while serving.
		{"release_failed outranks running", mod(func(in *envStateInput) {
			in.BuildPhase = "release-failed"
			in.Ready = 2
		}), stateReleaseFailed, ""},
		// Sleeping never claims an env with ready pods.
		{"sleep marker with ready pods is running", mod(func(in *envStateInput) {
			in.SleepMarked = true
		}), stateRunning, ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			state, detail := deriveEnvState(tc.in)
			if state != tc.wantState {
				t.Fatalf("state = %q, want %q (detail=%q)", state, tc.wantState, detail)
			}
			if tc.detailSubstr != "" && !strings.Contains(detail, tc.detailSubstr) {
				t.Fatalf("detail = %q, want substring %q", detail, tc.detailSubstr)
			}
		})
	}
}

// TestCrashLoopSignal covers the pod-informer scan: explicit
// CrashLoopBackOff, the restart-storm heuristic, and the pods that
// must NOT count (completed release-hook Jobs, terminating pods,
// healthy containers with historical restarts).
func TestCrashLoopSignal(t *testing.T) {
	t.Parallel()
	now := metav1.Now()
	old := metav1.NewTime(time.Now().Add(-time.Hour))
	pod := func(phase corev1.PodPhase, deleted bool, cs ...corev1.ContainerStatus) *corev1.Pod {
		p := &corev1.Pod{Status: corev1.PodStatus{Phase: phase, ContainerStatuses: cs}}
		if deleted {
			p.DeletionTimestamp = &now
		}
		return p
	}
	backoff := corev1.ContainerStatus{
		Name:         "app",
		RestartCount: 7,
		State:        corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
	}
	storm := corev1.ContainerStatus{
		Name:         "app",
		RestartCount: 5,
		LastTerminationState: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, FinishedAt: now},
		},
	}
	staleRestarts := corev1.ContainerStatus{
		Name:         "app",
		RestartCount: 9,
		LastTerminationState: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, FinishedAt: old},
		},
	}
	healthyWithHistory := corev1.ContainerStatus{Name: "app", Ready: true, RestartCount: 12}

	cases := []struct {
		name string
		pods []*corev1.Pod
		want bool
	}{
		{"crashloopbackoff detected", []*corev1.Pod{pod(corev1.PodRunning, false, backoff)}, true},
		{"restart storm detected", []*corev1.Pod{pod(corev1.PodRunning, false, storm)}, true},
		{"stale restarts ignored", []*corev1.Pod{pod(corev1.PodRunning, false, staleRestarts)}, false},
		{"ready container with history ignored", []*corev1.Pod{pod(corev1.PodRunning, false, healthyWithHistory)}, false},
		{"completed job pod ignored", []*corev1.Pod{pod(corev1.PodSucceeded, false, backoff)}, false},
		{"terminating pod ignored", []*corev1.Pod{pod(corev1.PodRunning, true, backoff)}, false},
		{"no pods", nil, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, detail := crashLoopSignal(tc.pods)
			if got != tc.want {
				t.Fatalf("crashLoopSignal = %v (detail=%q), want %v", got, detail, tc.want)
			}
			if got && detail == "" {
				t.Fatalf("positive signal must carry a detail string")
			}
		})
	}
}

// TestLatestBuildFor pins the build-selection rules: newest first,
// dry-runs skipped, branch-mismatched builds skipped.
func TestLatestBuildFor(t *testing.T) {
	t.Parallel()
	mk := func(name, branch string, dry bool) kube.KusoBuild {
		return kube.KusoBuild{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec:       kube.KusoBuildSpec{Service: "p-web", Branch: branch, DryRun: dry},
		}
	}
	env := &kube.KusoEnvironment{Spec: kube.KusoEnvironmentSpec{Service: "p-web", Branch: "main"}}
	builds := []kube.KusoBuild{ // newest first, as buildsByService returns
		mk("b4-dry", "main", true),
		mk("b3-staging", "staging", false),
		mk("b2-main", "main", false),
		mk("b1-main", "main", false),
	}
	got := latestBuildFor(env, builds)
	if got == nil || got.Name != "b2-main" {
		t.Fatalf("latestBuildFor = %+v, want b2-main (dry-run + staging skipped)", got)
	}
	// Env without a branch accepts the newest non-dry build.
	anyBranch := &kube.KusoEnvironment{Spec: kube.KusoEnvironmentSpec{Service: "p-web"}}
	if got := latestBuildFor(anyBranch, builds); got == nil || got.Name != "b3-staging" {
		t.Fatalf("branchless env: got %+v, want b3-staging", got)
	}
	if got := latestBuildFor(env, nil); got != nil {
		t.Fatalf("no builds must yield nil, got %+v", got)
	}
}
