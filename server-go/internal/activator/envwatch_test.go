package activator

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuso/server/internal/kube"
)

func envObj(name, host string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "application.kuso.sislelabs.com/v1alpha1",
		"kind":       "KusoEnvironment",
		"metadata": map[string]any{"name": name, "namespace": "kuso", "annotations": map[string]any{
			"kuso.sislelabs.com/last-activity": "2026-11-14T10:00:00Z",
			"unrelated":                        "x",
		}},
		"spec":   map[string]any{"host": host, "stopped": true, "image": map[string]any{"tag": "abc"}},
		"status": map[string]any{"deployedRelease": map[string]any{"manifest": "big"}},
	}}
}

func newEnvDyn(t *testing.T, objs ...*unstructured.Unstructured) *dynamicfake.FakeDynamicClient {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{kube.GVREnvironments: "KusoEnvironmentList"})
	for _, o := range objs {
		if err := dyn.Tracker().Create(kube.GVREnvironments, o, o.GetNamespace()); err != nil {
			t.Fatal(err)
		}
	}
	return dyn
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// SCL-1: once the informer syncs, host-index refreshes are served from the
// watch cache, not a live LIST each time.
func TestEnvSource_SteadyStateDoesNoList(t *testing.T) {
	t.Parallel()
	dyn := newEnvDyn(t, envObj("a-web-production", "web.a.example.com"))
	var liveCalls atomic.Int64
	live := func(ctx context.Context) ([]kube.KusoEnvironment, error) {
		liveCalls.Add(1)
		return (&kube.Client{Dynamic: dyn}).ListKusoEnvironments(ctx, "")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src := startEnvSource(ctx, dyn, live, slog.Default())
	waitFor(t, "informer sync", src.inf.HasSynced)

	var apiLists atomic.Int64
	dyn.PrependReactor("list", "*", func(k8stesting.Action) (bool, runtime.Object, error) {
		apiLists.Add(1)
		return false, nil, nil
	})
	liveCalls.Store(0)
	for range 20 {
		envs, err := src.list(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(envs) != 1 || envs[0].Spec.Host != "web.a.example.com" || !envs[0].Spec.Stopped {
			t.Fatalf("got %+v", envs)
		}
	}
	if n := liveCalls.Load() + apiLists.Load(); n != 0 {
		t.Fatalf("steady state did %d live LISTs, want 0", n)
	}

	// A new env arrives through the watch.
	if err := dyn.Tracker().Create(kube.GVREnvironments, envObj("a-web-pr-1", "pr-1.a.example.com"), "kuso"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "watch delivery", func() bool {
		envs, _ := src.list(ctx)
		return len(envs) == 2
	})

	// The cached copy is slim: no status, no unread spec, only the activity annotation.
	o, ok, _ := src.inf.GetStore().GetByKey("kuso/a-web-production")
	if !ok {
		t.Fatal("env missing from store")
	}
	u := o.(*unstructured.Unstructured)
	if _, has := u.Object["status"]; has {
		t.Error("status kept in cache")
	}
	if _, has, _ := unstructured.NestedFieldNoCopy(u.Object, "spec", "image"); has {
		t.Error("spec.image kept in cache")
	}
	if ann := u.GetAnnotations(); len(ann) != 1 || ann["kuso.sislelabs.com/last-activity"] == "" {
		t.Errorf("annotations = %v, want only last-activity", ann)
	}
}

// Upgrades can land before the hand-applied ClusterRole grants watch: a
// forbidden watch must fall back to the live LIST, not freeze the index.
func TestEnvSource_WatchForbiddenFallsBackToList(t *testing.T) {
	t.Parallel()
	dyn := newEnvDyn(t, envObj("a-web-production", "web.a.example.com"))
	dyn.PrependWatchReactor("*", func(k8stesting.Action) (bool, watch.Interface, error) {
		return true, nil, apierrors.NewForbidden(kube.GVREnvironments.GroupResource(), "", nil)
	})
	var liveCalls atomic.Int64
	live := func(ctx context.Context) ([]kube.KusoEnvironment, error) {
		liveCalls.Add(1)
		return (&kube.Client{Dynamic: dyn}).ListKusoEnvironments(ctx, "")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src := startEnvSource(ctx, dyn, live, slog.Default())
	waitFor(t, "watch denial", src.denied.Load)

	before := liveCalls.Load()
	envs, err := src.list(ctx)
	if err != nil || len(envs) != 1 {
		t.Fatalf("list = %v, %v", envs, err)
	}
	if liveCalls.Load() != before+1 {
		t.Fatal("forbidden watch did not fall back to the live LIST")
	}
}
