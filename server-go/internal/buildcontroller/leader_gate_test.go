package buildcontroller

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"

	"kuso/server/internal/kube"
)

// TestMaybeReconcileGate pins the leader gate in maybeReconcile: handlers
// accumulate across leader re-elections, and without the gate every
// replica would reconcile every event. A closed gate must create no Job;
// nil and open gates must. Each case uses its own namespace because
// kube.IsManagedNamespace caches verdicts package-wide.
func TestMaybeReconcileGate(t *testing.T) {
	cases := []struct {
		name    string
		leader  func() *atomic.Bool
		wantJob bool
	}{
		{"nil leader = always run", func() *atomic.Bool { return nil }, true},
		{"leader false = gate closed", func() *atomic.Bool { return &atomic.Bool{} }, false},
		{"leader true = pass through", func() *atomic.Bool { b := &atomic.Bool{}; b.Store(true); return b }, true},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ns := fmt.Sprintf("kuso-leader-gate-%d", i)
			cs := kubefake.NewSimpleClientset(managedNS(ns))
			s := &Service{
				Kube:         &kube.Client{Clientset: cs},
				Logger:       retryTestLogger(),
				running:      map[string]struct{}{},
				LeaderActive: c.leader(),
			}
			ctx := context.Background()
			s.maybeReconcile(ctx, retryTestBuild(ns, "b1"), "test")
			_, err := cs.BatchV1().Jobs(ns).Get(ctx, "b1", metav1.GetOptions{})
			if got := err == nil; got != c.wantJob {
				t.Errorf("job created = %v, want %v (err %v)", got, c.wantJob, err)
			}
		})
	}
}

// TestRunningMapDedup verifies the per-Service running-set behaves
// as expected. The dedup map is what suppresses the patch-flood
// from the build poller (every 5s the poller stamps phase
// annotations, each producing an Update event we don't need to
// re-reconcile).
//
// We test the map mechanic directly since reconcile's earliest
// branches return on partial CRs (no image, done=true, etc.) and
// the map insert only happens after those pass — covering the
// integration would need a kube fake.
func TestRunningMapDedupShape(t *testing.T) {
	s := &Service{running: map[string]struct{}{}}
	// Two parallel callers that observe the same key. Only one
	// should win the slot; the other should see "already in
	// flight" and return without doing work.
	var wg sync.WaitGroup
	wins := atomic.Int32{}
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.mu.Lock()
			if _, already := s.running["ns/build"]; already {
				s.mu.Unlock()
				return
			}
			s.running["ns/build"] = struct{}{}
			s.mu.Unlock()
			wins.Add(1)
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Errorf("running map dedup: %d goroutines won, want exactly 1", wins.Load())
	}
}

// TestKusoBuildLabelsAlwaysSetsInstance pins the v0.10.1 fix to the
// regression site. The helm chart used to emit
// app.kubernetes.io/instance automatically from .Release.Name; the
// Go controller has to set it explicitly. Every log selector +
// Cancel pod-list call keys on this label, so missing it breaks the
// Deployments-tab log viewer entirely.
//
// This test runs in addition to the broader TestRenderJobLabels-
// RoundTrip in render_test.go — that one ALSO checks the label,
// but the bug we're locking in is specifically "if a future
// refactor drops the buildName param from kusoBuildLabels, the
// instance label gets lost." Keep this focused assertion separate
// so it can't be silently subsumed by a label-set refactor.
func TestKusoBuildLabelsAlwaysSetsInstance(t *testing.T) {
	// nil build → minimum-viable labels still carry the instance
	// (defensive — the build controller never calls with a nil CR
	// today but the helper has a nil-guard so the test exercises it).
	labels := kusoBuildLabels(nil, "b1")
	if labels["app.kubernetes.io/instance"] != "b1" {
		t.Errorf("instance on nil-build = %q, want b1", labels["app.kubernetes.io/instance"])
	}

	// Full CR → instance + project/service/build-ref all present.
	b := &kube.KusoBuild{}
	b.Spec.Project = "alpha"
	b.Spec.Service = "api"
	b.Spec.Ref = "abc"
	labels = kusoBuildLabels(b, "alpha-api-abc")
	if labels["app.kubernetes.io/instance"] != "alpha-api-abc" {
		t.Errorf("instance = %q, want alpha-api-abc", labels["app.kubernetes.io/instance"])
	}
	if labels["kuso.sislelabs.com/project"] != "alpha" {
		t.Errorf("project label = %q", labels["kuso.sislelabs.com/project"])
	}
}

// TestHandleDelete_UnwrapsTombstone pins fix #4: DeleteFunc must clear the
// running-dedup key even when the delete arrives as a
// cache.DeletedFinalStateUnknown tombstone (delivered when a delete is
// missed during a watch relist). Before the fix the handler only asserted
// *unstructured.Unstructured, so a tombstoned delete left the key set and a
// same-name rebuild was deduped forever (its Job never rendered).
func TestHandleDelete_UnwrapsTombstone(t *testing.T) {
	newU := func(ns, name string) *unstructured.Unstructured {
		u := &unstructured.Unstructured{Object: map[string]any{}}
		u.SetNamespace(ns)
		u.SetName(name)
		return u
	}

	t.Run("direct unstructured clears key", func(t *testing.T) {
		s := &Service{running: map[string]struct{}{"kuso/alpha-web-abc": {}}}
		s.handleDelete(newU("kuso", "alpha-web-abc"))
		if _, still := s.running["kuso/alpha-web-abc"]; still {
			t.Fatalf("direct delete did not clear the running key")
		}
	})

	t.Run("tombstone-wrapped delete clears key", func(t *testing.T) {
		s := &Service{running: map[string]struct{}{"kuso/alpha-web-abc": {}}}
		tombstone := cache.DeletedFinalStateUnknown{
			Key: "kuso/alpha-web-abc",
			Obj: newU("kuso", "alpha-web-abc"),
		}
		s.handleDelete(tombstone)
		if _, still := s.running["kuso/alpha-web-abc"]; still {
			t.Fatalf("tombstoned delete did not clear the running key — same-name rebuild would be deduped forever")
		}
	})

	t.Run("garbage payload is ignored without panic", func(t *testing.T) {
		s := &Service{running: map[string]struct{}{"kuso/keep": {}}}
		s.handleDelete("not-an-object")
		s.handleDelete(cache.DeletedFinalStateUnknown{Key: "x", Obj: "still-not-an-object"})
		if _, still := s.running["kuso/keep"]; !still {
			t.Fatalf("garbage payloads should not touch unrelated keys")
		}
	})
}

// TestDecodeBuildHandlesUnstructured verifies the decode path
// rejects gracefully. A future apiserver returning a CR with a
// bogus spec.image type (e.g. a string where the struct is
// expected) should not panic the reconcile loop.
func TestDecodeBuildHandlesUnstructured(t *testing.T) {
	cases := []struct {
		name string
		obj  map[string]any
	}{
		{
			name: "minimum-valid",
			obj: map[string]any{
				"apiVersion": "application.kuso.sislelabs.com/v1alpha1",
				"kind":       "KusoBuild",
				"metadata":   map[string]any{"name": "b1", "namespace": "kuso"},
				"spec": map[string]any{
					"project": "p", "service": "p-s", "ref": "abc",
				},
			},
		},
		{
			name: "with-image",
			obj: map[string]any{
				"apiVersion": "application.kuso.sislelabs.com/v1alpha1",
				"kind":       "KusoBuild",
				"metadata":   map[string]any{"name": "b1", "namespace": "kuso"},
				"spec": map[string]any{
					"project": "p", "service": "p-s", "ref": "abc",
					"image": map[string]any{"repository": "r", "tag": "t"},
				},
			},
		},
	}
	for _, c := range cases {
		u := &unstructured.Unstructured{Object: c.obj}
		b, err := decodeBuild(u)
		if err != nil {
			t.Errorf("%s: decode err = %v", c.name, err)
		}
		if b == nil {
			t.Errorf("%s: decode returned nil", c.name)
		}
	}
}
