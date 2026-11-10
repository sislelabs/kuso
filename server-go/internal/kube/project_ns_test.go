package kube

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

// A transient lookup error must not pin a custom-namespace project to the
// home namespace for the TTL (SCL-2).
func TestNamespaceFor_TransientErrorNotCachedAsHome(t *testing.T) {
	t.Parallel()
	c := fakeClient(t, seed(GVRProjects, "KusoProject", "kuso", "acme",
		map[string]any{"namespace": "kuso-acme"}))
	dyn := c.Dynamic.(*dynamicfake.FakeDynamicClient)
	var failing atomic.Bool
	dyn.PrependReactor("get", "kusoprojects", func(k8stesting.Action) (bool, runtime.Object, error) {
		if failing.Load() {
			return true, nil, errors.New("context deadline exceeded")
		}
		return false, nil, nil
	})
	r := NewProjectNamespaceResolver(c, "kuso")
	ctx := context.Background()

	failing.Store(true)
	if got := r.NamespaceFor(ctx, "acme"); got != "kuso" {
		t.Fatalf("cold cache + error: got %q, want home fallback", got)
	}
	failing.Store(false)
	if got := r.NamespaceFor(ctx, "acme"); got != "kuso-acme" {
		t.Fatalf("after recovery: got %q, want kuso-acme (error result must not be cached)", got)
	}

	// Expired entry + error: keep the last known answer.
	r.mu.Lock()
	e := r.cache["acme"]
	e.expires = e.expires.Add(-2 * r.TTL)
	r.cache["acme"] = e
	r.mu.Unlock()
	failing.Store(true)
	if got := r.NamespaceFor(ctx, "acme"); got != "kuso-acme" {
		t.Fatalf("expired entry + error: got %q, want last known kuso-acme", got)
	}
}

func TestNamespaceFor_NotFoundCachesHome(t *testing.T) {
	t.Parallel()
	c := fakeClient(t)
	r := NewProjectNamespaceResolver(c, "kuso")
	if got := r.NamespaceFor(context.Background(), "ghost"); got != "kuso" {
		t.Fatalf("got %q, want kuso", got)
	}
	r.mu.RLock()
	_, cached := r.cache["ghost"]
	r.mu.RUnlock()
	if !cached {
		t.Fatal("NotFound is definitive and should be cached")
	}
}
