package buildcontroller

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuso/server/internal/builds"
	"kuso/server/internal/kube"
)

// Retry-path tests (resilience W5). Before the backoff ladder existed,
// a failed ensureJob just dropped the dedup key and hoped for another
// informer event — which never arrives for a fresh CR nobody patches.
// These tests drive reconcile() against a fake clientset whose Job
// create fails N times, and assert the controller retries with backoff,
// converges, and gives up loudly (with state fully cleared) when the
// failure is permanent.
//
// NOTE on namespaces: kube.IsManagedNamespace caches per-namespace
// verdicts in a package-global 30s TTL map, so every test here uses a
// unique namespace name to avoid cross-test pollution.

func retryTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func managedNS(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   name,
		Labels: map[string]string{kube.ManagedByLabel: kube.ManagedByValue},
	}}
}

// retryTestBuild builds a minimal-but-valid KusoBuild unstructured that
// passes every early-return guard in reconcile (not done, image+repo
// set, no promote-hold).
func retryTestBuild(ns, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "application.kuso.sislelabs.com/v1alpha1",
		"kind":       "KusoBuild",
		"metadata":   map[string]any{"name": name, "namespace": ns, "uid": "test-uid"},
		"spec": map[string]any{
			"project": "p", "service": "p-s", "ref": "abc",
			"image": map[string]any{"repository": "reg/p/s", "tag": "t1"},
			"repo":  map[string]any{"url": "https://example.com/r.git"},
		},
	}}
}

func retrySnapshot(s *Service) (nRetries, nRunning int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.retries), len(s.running)
}

// waitFor polls cond until true or the deadline passes.
func waitFor(t *testing.T, d time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return cond()
}

// TestRetryEventuallySucceeds: Job create fails twice (transient
// apiserver blip), the backoff ladder re-fires reconcile, and the third
// attempt lands the Job. Retry state must be fully cleared afterwards.
func TestRetryEventuallySucceeds(t *testing.T) {
	ns := "kuso-retry-succeeds"
	cs := kubefake.NewSimpleClientset(managedNS(ns))
	var jobCreates atomic.Int32
	cs.PrependReactor("create", "jobs", func(_ k8stesting.Action) (bool, runtime.Object, error) {
		if n := jobCreates.Add(1); n <= 2 {
			return true, nil, fmt.Errorf("simulated apiserver blip %d", n)
		}
		return false, nil, nil // fall through to the tracker's real create
	})
	s := &Service{
		Kube:      &kube.Client{Clientset: cs},
		Logger:    retryTestLogger(),
		running:   map[string]struct{}{},
		retryBase: 2 * time.Millisecond,
	}
	ctx := context.Background()
	s.reconcile(ctx, retryTestBuild(ns, "b1"), "add")

	ok := waitFor(t, 5*time.Second, func() bool {
		_, err := cs.BatchV1().Jobs(ns).Get(ctx, "b1", metav1.GetOptions{})
		return err == nil
	})
	if !ok {
		t.Fatalf("job never created after retries (create attempts: %d)", jobCreates.Load())
	}
	if got := jobCreates.Load(); got != 3 {
		t.Errorf("job create attempts = %d, want 3 (1 initial + 2 retries)", got)
	}
	// Success must clear the backoff ledger; the running key stays (the
	// Job exists, dedup is correct).
	if !waitFor(t, time.Second, func() bool { nr, _ := retrySnapshot(s); return nr == 0 }) {
		nr, _ := retrySnapshot(s)
		t.Errorf("retry state not cleared after success: %d entries", nr)
	}
}

// TestRetryGivesUpAfterMaxAttempts: a permanent failure exhausts the
// ladder — exactly 1+retryMax create attempts happen, then the state is
// cleared and NO further attempts fire. Kube.Dynamic is nil here so the
// give-up terminal stamp is skipped (guarded); the loud slog is the
// signal under test's control.
func TestRetryGivesUpAfterMaxAttempts(t *testing.T) {
	ns := "kuso-retry-givesup"
	cs := kubefake.NewSimpleClientset(managedNS(ns))
	var jobCreates atomic.Int32
	cs.PrependReactor("create", "jobs", func(_ k8stesting.Action) (bool, runtime.Object, error) {
		jobCreates.Add(1)
		return true, nil, fmt.Errorf("permanent failure")
	})
	s := &Service{
		Kube:      &kube.Client{Clientset: cs},
		Logger:    retryTestLogger(),
		running:   map[string]struct{}{},
		retryBase: time.Millisecond,
		retryMax:  2,
	}
	ctx := context.Background()
	s.reconcile(ctx, retryTestBuild(ns, "b2"), "add")

	// 1 initial + 2 retries, then give-up.
	if !waitFor(t, 5*time.Second, func() bool { return jobCreates.Load() >= 3 }) {
		t.Fatalf("expected 3 create attempts, got %d", jobCreates.Load())
	}
	if !waitFor(t, time.Second, func() bool { nr, _ := retrySnapshot(s); return nr == 0 }) {
		t.Fatalf("retry state not cleared after give-up")
	}
	// Settle: no zombie timer may add attempts after give-up.
	time.Sleep(50 * time.Millisecond)
	if got := jobCreates.Load(); got != 3 {
		t.Errorf("create attempts after give-up = %d, want exactly 3", got)
	}
	// The running key must be free so a manual retrigger (new CR event)
	// can reconcile.
	if _, nRunning := retrySnapshot(s); nRunning != 0 {
		t.Errorf("running key still held after give-up")
	}
}

// TestRetryPendingTimerCancelledByInformerSuccess pins double-enqueue
// safety: while a retry timer is pending, the informer-event path
// re-reconciles and succeeds — the pending timer must be cancelled so
// it can't fire a redundant reconcile later.
func TestRetryPendingTimerCancelledByInformerSuccess(t *testing.T) {
	ns := "kuso-retry-cancelled"
	cs := kubefake.NewSimpleClientset(managedNS(ns))
	var jobCreates atomic.Int32
	cs.PrependReactor("create", "jobs", func(_ k8stesting.Action) (bool, runtime.Object, error) {
		if jobCreates.Add(1) == 1 {
			return true, nil, fmt.Errorf("first attempt fails")
		}
		return false, nil, nil
	})
	s := &Service{
		Kube:      &kube.Client{Clientset: cs},
		Logger:    retryTestLogger(),
		running:   map[string]struct{}{},
		retryBase: 10 * time.Second, // long enough to never fire in-test
	}
	ctx := context.Background()
	u := retryTestBuild(ns, "b3")
	s.reconcile(ctx, u, "add") // fails, arms a 10s retry
	if nr, _ := retrySnapshot(s); nr != 1 {
		t.Fatalf("expected 1 pending retry entry, got %d", nr)
	}
	s.reconcile(ctx, u, "update") // informer event succeeds
	if _, err := cs.BatchV1().Jobs(ns).Get(ctx, "b3", metav1.GetOptions{}); err != nil {
		t.Fatalf("job not created by informer-event path: %v", err)
	}
	if nr, _ := retrySnapshot(s); nr != 0 {
		t.Errorf("pending retry not cancelled after informer-event success: %d entries", nr)
	}
	if got := jobCreates.Load(); got != 2 {
		t.Errorf("create attempts = %d, want 2", got)
	}
}

// TestScheduleRetryKeepsExistingTimer: a second failure while a retry
// is already queued must not stack another timer or advance the
// attempt counter (the queued retry owns the next move).
func TestScheduleRetryKeepsExistingTimer(t *testing.T) {
	s := &Service{Logger: retryTestLogger(), retryBase: 10 * time.Second}
	u := retryTestBuild("kuso-retry-keep", "b4")
	key := "kuso-retry-keep/b4"
	ctx := context.Background()
	s.scheduleRetry(ctx, key, u)
	s.scheduleRetry(ctx, key, u)
	s.mu.Lock()
	st := s.retries[key]
	attempts := 0
	if st != nil {
		attempts = st.attempts
	}
	s.mu.Unlock()
	defer s.clearRetry(key)
	if attempts != 1 {
		t.Errorf("attempts after double-schedule = %d, want 1 (existing timer kept)", attempts)
	}
}

// TestGiveUpStampsTerminalDoneState is the regression test for the wedged
// build-queue bug: giveUp stamped phase=failed + spec.done=true but FORGOT
// the build-state=done label every other terminal path stamps. Without the
// label the CR still counts as in-flight to admission's active-build check
// (queueing every later build for the service forever), the poller's
// stuck-build healer skips phase=failed, and retention sweeps (which select
// build-state=done) can never delete it. giveUp must produce a CR that (a)
// admission counts as NOT in-flight and (b) retention CAN select + delete.
func TestGiveUpStampsTerminalDoneState(t *testing.T) {
	ns := "kuso-giveup-terminal"
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		kube.GVRBuilds: "KusoBuildList",
	})
	u := retryTestBuild(ns, "b6")
	// Old enough that the retention sweep's age cutoff selects it.
	u.SetCreationTimestamp(metav1.NewTime(time.Now().Add(-48 * time.Hour)))
	// Labels as builds.Add stamps them (project/service); build-state
	// deliberately absent = in-flight.
	u.SetLabels(map[string]string{
		"kuso.sislelabs.com/project": "p",
		"kuso.sislelabs.com/service": "p-s",
	})
	if err := dyn.Tracker().Create(kube.GVRBuilds, u, ns); err != nil {
		t.Fatalf("seed build: %v", err)
	}
	kc := &kube.Client{Dynamic: dyn}
	s := &Service{Kube: kc, Logger: retryTestLogger()}

	ctx := context.Background()
	s.giveUp(ctx, u, retryMaxAttempts)

	got, err := dyn.Resource(kube.GVRBuilds).Namespace(ns).Get(ctx, "b6", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get build after giveUp: %v", err)
	}
	// (a) Admission's in-flight predicate is `build-state label absent`
	// (builds/admission.go findActiveForService) — the label must be the
	// terminal marker every other terminal path stamps.
	if st := got.GetLabels()[builds.LabelBuildState]; st != builds.BuildStateDone {
		t.Errorf("build-state label = %q, want %q — admission still counts this build as in-flight, wedging the service's build queue",
			st, builds.BuildStateDone)
	}
	annos := got.GetAnnotations()
	if annos[builds.AnnBuildPhase] != "failed" {
		t.Errorf("build-phase = %q, want failed", annos[builds.AnnBuildPhase])
	}
	if annos[builds.AnnBuildMessage] == "" {
		t.Error("build-message must carry the give-up cause")
	}
	if annos[builds.AnnBuildCompletedAt] == "" {
		t.Error("build-completed-at must be stamped")
	}
	if done, _, _ := unstructured.NestedBool(got.Object, "spec", "done"); !done {
		t.Error("spec.done must be true")
	}

	// (b) Retention must be able to select + delete the given-up CR.
	deleted, err := builds.SweepFinishedBuilds(ctx, kc, ns, time.Hour, nil)
	if err != nil {
		t.Fatalf("SweepFinishedBuilds: %v", err)
	}
	if deleted != 1 {
		t.Errorf("retention sweep deleted %d builds, want 1 — a given-up build must be sweepable", deleted)
	}
}

// TestReconcileCASBusyReArmsPendingRetry pins the retry-ledger fix: when a
// retry fires into the running-key CAS while another reconcile holds the
// key, the timer-less ledger entry must be RE-ARMED, not dropped. Dropped,
// it stranded a stale attempts count that shortened a future unrelated
// failure's backoff ladder (and the fire itself was silently lost).
func TestReconcileCASBusyReArmsPendingRetry(t *testing.T) {
	ns := "kuso-retry-casbusy"
	cs := kubefake.NewSimpleClientset(managedNS(ns))
	s := &Service{
		Kube:      &kube.Client{Clientset: cs},
		Logger:    retryTestLogger(),
		running:   map[string]struct{}{},
		retryBase: 10 * time.Second, // long enough to never fire in-test
	}
	u := retryTestBuild(ns, "b7")
	key := ns + "/b7"
	// Simulate: an informer-event reconcile currently owns the running key…
	s.running[key] = struct{}{}
	// …and a retry timer has just fired into that race (retryFire nils the
	// timer before re-entering reconcile).
	s.retries = map[string]*retryState{key: {attempts: 2, obj: u}}
	defer s.clearRetry(key)

	s.reconcile(context.Background(), u, "retry")

	s.mu.Lock()
	st := s.retries[key]
	var timerArmed bool
	attempts := 0
	if st != nil {
		timerArmed = st.timer != nil
		attempts = st.attempts
	}
	s.mu.Unlock()
	if st == nil {
		t.Fatal("retry entry deleted on CAS-busy — the holder still owes an outcome")
	}
	if !timerArmed {
		t.Error("retry entry left timer-less on CAS-busy (the stranded-ledger bug): stale attempts persist and the fire is lost")
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2 — losing the CAS race is not an ensureJob failure", attempts)
	}
}

// TestBackoffLadder pins the exponential shape and the 5-minute cap.
func TestBackoffLadder(t *testing.T) {
	s := &Service{}
	want := []time.Duration{
		5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second,
		80 * time.Second, 160 * time.Second, 5 * time.Minute, 5 * time.Minute,
	}
	for i, w := range want {
		if got := s.backoffFor(i + 1); got != w {
			t.Errorf("backoffFor(%d) = %v, want %v", i+1, got, w)
		}
	}
}
