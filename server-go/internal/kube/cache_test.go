package kube

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestCache_FallbackWhenNil proves that without a Cache attached, list[T]
// goes straight to the live dynamic client. This is the bare-bones
// constructor path used by the CLI and by tests that don't want a watch.
func TestCache_FallbackWhenNil(t *testing.T) {
	t.Parallel()
	c := fakeClient(t, seed(GVRProjects, "KusoProject", "kuso", "alpha", map[string]any{
		"baseDomain": "alpha.example.com",
	}))
	if c.Cache != nil {
		t.Fatalf("fresh fakeClient should have nil Cache, got %T", c.Cache)
	}

	got, err := c.ListKusoProjects(context.Background(), "kuso")
	if err != nil {
		t.Fatalf("ListKusoProjects: %v", err)
	}
	if len(got) != 1 || got[0].Name != "alpha" {
		t.Fatalf("want 1 project named alpha, got %+v", got)
	}
}

// TestCache_HitReturnsCachedData walks the full cache lifecycle: attach
// the informer, start it, wait for the initial sync, then verify reads
// return the seeded objects. This is the happy path the kuso server runs
// in production.
func TestCache_HitReturnsCachedData(t *testing.T) {
	t.Parallel()
	c := fakeClient(t,
		seed(GVRProjects, "KusoProject", "kuso", "alpha", map[string]any{"baseDomain": "alpha.example.com"}),
		seed(GVRProjects, "KusoProject", "kuso", "beta", map[string]any{"baseDomain": "beta.example.com"}),
	)

	c.EnableCache()
	t.Cleanup(func() { c.Cache.Stop() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !c.Cache.WaitForSync(ctx) {
		t.Fatal("informer never synced")
	}

	got, err := c.ListKusoProjects(ctx, "kuso")
	if err != nil {
		t.Fatalf("ListKusoProjects: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 projects, got %d", len(got))
	}
	names := map[string]bool{}
	for _, p := range got {
		names[p.Name] = true
	}
	if !names["alpha"] || !names["beta"] {
		t.Errorf("missing names; got %v", names)
	}
}

// TestCache_NamespaceFilter ensures the cache respects the namespace
// argument: a CR in ns "other" is invisible to a list scoped to "kuso".
// The cache watches cluster-wide; the namespace filter is applied at
// read time, and we want to be sure that filter actually filters.
func TestCache_NamespaceFilter(t *testing.T) {
	t.Parallel()
	c := fakeClient(t,
		seed(GVRProjects, "KusoProject", "kuso", "alpha", nil),
		seed(GVRProjects, "KusoProject", "other", "gamma", nil),
	)
	c.EnableCache()
	t.Cleanup(func() { c.Cache.Stop() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !c.Cache.WaitForSync(ctx) {
		t.Fatal("informer never synced")
	}

	in, err := c.ListKusoProjects(ctx, "kuso")
	if err != nil {
		t.Fatalf("list kuso: %v", err)
	}
	if len(in) != 1 || in[0].Name != "alpha" {
		t.Errorf("kuso ns: want [alpha], got %+v", in)
	}

	out, err := c.ListKusoProjects(ctx, "other")
	if err != nil {
		t.Fatalf("list other: %v", err)
	}
	if len(out) != 1 || out[0].Name != "gamma" {
		t.Errorf("other ns: want [gamma], got %+v", out)
	}
}

// TestCache_DeepCopyIsolation verifies the cache hands back independent
// copies. A caller that mutates a returned struct must not corrupt the
// next caller's view. Without DeepCopy in listFromCache this would fail.
func TestCache_DeepCopyIsolation(t *testing.T) {
	t.Parallel()
	c := fakeClient(t,
		seed(GVRProjects, "KusoProject", "kuso", "alpha", map[string]any{
			"baseDomain": "alpha.example.com",
		}),
	)
	c.EnableCache()
	t.Cleanup(func() { c.Cache.Stop() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !c.Cache.WaitForSync(ctx) {
		t.Fatal("informer never synced")
	}

	first, err := c.ListKusoProjects(ctx, "kuso")
	if err != nil {
		t.Fatalf("first list: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("want 1, got %d", len(first))
	}
	// Stomp the returned struct.
	first[0].Spec.BaseDomain = "MUTATED"

	second, err := c.ListKusoProjects(ctx, "kuso")
	if err != nil {
		t.Fatalf("second list: %v", err)
	}
	if len(second) != 1 {
		t.Fatalf("want 1, got %d", len(second))
	}
	if second[0].Spec.BaseDomain != "alpha.example.com" {
		t.Errorf("cache leaked mutation across reads: BaseDomain=%q (want alpha.example.com)",
			second[0].Spec.BaseDomain)
	}
}

// TestCache_SelectorBypassesCache exercises the metav1.ListOptions
// label-selector escape hatch. listFromCache today serves only
// no-selector reads; anything with LabelSelector or FieldSelector
// must transparently route to the live API.
//
// We construct this scenario by enabling the cache, draining its watch,
// then issuing a selector-bearing list against a typed entry point
// internal to this package (we use the generic list[T] indirectly via
// the metav1.ListOptions plumbing exposed only in tests).
func TestCache_SelectorBypassesCache(t *testing.T) {
	t.Parallel()
	c := fakeClient(t,
		seed(GVRProjects, "KusoProject", "kuso", "alpha", nil),
	)
	c.EnableCache()
	t.Cleanup(func() { c.Cache.Stop() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !c.Cache.WaitForSync(ctx) {
		t.Fatal("informer never synced")
	}

	// Direct call to the generic helper with a non-empty selector. The
	// cache branch should be skipped per the if guard in list[T] —
	// listFromCache today serves only no-selector reads. The fake
	// client honours label selectors against the underlying tracker;
	// our seeded project has no labels, so the selector matches nothing.
	// The result we care about: no panic (proves the cache branch was
	// skipped cleanly), and 0 results (proves the live path was taken,
	// since the cache would have returned the seeded item ignoring the
	// selector).
	got, err := list[KusoProject](ctx, c, GVRProjects, "kuso", metav1.ListOptions{
		LabelSelector: "app=nonexistent",
	})
	if err != nil {
		t.Fatalf("selector list: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("selector should have routed to live API and matched zero objects; got %d", len(got))
	}

	// Sanity check: the same call without a selector hits the cache and
	// returns the seeded item.
	gotAll, err := list[KusoProject](ctx, c, GVRProjects, "kuso", metav1.ListOptions{})
	if err != nil {
		t.Fatalf("no-selector list: %v", err)
	}
	if len(gotAll) != 1 || gotAll[0].Name != "alpha" {
		t.Errorf("cache path: want [alpha], got %+v", gotAll)
	}
}

// TestCache_StopIsIdempotent guards against accidental panics if a
// shutdown path closes the stop channel twice (e.g. signal handler +
// defer in main).
func TestCache_StopIsIdempotent(t *testing.T) {
	t.Parallel()
	c := fakeClient(t)
	c.EnableCache()
	c.Cache.Stop()
	c.Cache.Stop() // must not panic on double-close
}
