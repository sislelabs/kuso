package activator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuso/server/internal/kube"
)

// Non-production envs sleep by default, so the activator has to resolve
// (and wake) their hosts, not only production's.
func TestResolveByHost_NonProductionEnvs(t *testing.T) {
	t.Parallel()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{kube.GVREnvironments: "KusoEnvironmentList"})
	seed := func(ns, name, kind, host string) {
		u := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "application.kuso.sislelabs.com/v1alpha1",
			"kind":       "KusoEnvironment",
			"metadata":   map[string]any{"name": name, "namespace": ns},
			"spec":       map[string]any{"kind": kind, "host": host},
		}}
		if err := dyn.Tracker().Create(kube.GVREnvironments, u, ns); err != nil {
			t.Fatal(err)
		}
	}
	seed("kuso", "alpha-web-production", "production", "web.alpha.example.com")
	seed("kuso", "alpha-web-pr-7", "preview", "web-pr-7.alpha.example.com")
	seed("kuso-beta", "beta-api-staging", "custom", "api-staging.beta.example.com")

	a := New(&kube.Client{Dynamic: dyn}, slog.Default())
	cases := []struct{ host, env, ns string }{
		{"web.alpha.example.com", "alpha-web-production", "kuso"},
		{"web-pr-7.alpha.example.com", "alpha-web-pr-7", "kuso"},
		{"api-staging.beta.example.com", "beta-api-staging", "kuso-beta"},
	}
	for _, c := range cases {
		env, ns, stopped, err := a.resolveByHost(context.Background(), c.host)
		if err != nil {
			t.Errorf("%s: resolve: %v", c.host, err)
			continue
		}
		if env != c.env || ns != c.ns || stopped {
			t.Errorf("%s: got (%s, %s, stopped=%v), want (%s, %s, false)", c.host, env, ns, stopped, c.env, c.ns)
		}
	}
}

// A slept env reachable only through a wildcard domain must still wake:
// standard one-label wildcard semantics, exact beats wildcard, and the
// most specific wildcard wins.
func TestResolveByHost_Wildcard(t *testing.T) {
	t.Parallel()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{kube.GVREnvironments: "KusoEnvironmentList"})
	seed := func(name, host string, wildcards ...string) {
		wd := make([]any, 0, len(wildcards))
		for _, w := range wildcards {
			wd = append(wd, map[string]any{"host": w, "tlsSecret": "wc-tls"})
		}
		u := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "application.kuso.sislelabs.com/v1alpha1",
			"kind":       "KusoEnvironment",
			"metadata":   map[string]any{"name": name, "namespace": "kuso"},
			"spec":       map[string]any{"kind": "production", "host": host, "wildcardDomains": wd},
		}}
		if err := dyn.Tracker().Create(kube.GVREnvironments, u, "kuso"); err != nil {
			t.Fatal(err)
		}
	}
	// Order matters: the wildcard envs are listed (sorted) before "z-exact" so a
	// first-match implementation would wrongly pick "a-narrow" for
	// a.shop.example.com.
	seed("broad", "broad.internal", "*.example.com")
	seed("a-narrow", "narrow.internal", "*.shop.example.com")
	seed("z-exact", "a.shop.example.com")

	a := New(&kube.Client{Dynamic: dyn}, slog.Default())
	cases := []struct{ host, env string }{
		{"a.example.com", "broad"},
		{"A.Example.COM", "broad"},
		{"b.shop.example.com", "a-narrow"},
		{"a.shop.example.com", "z-exact"},
	}
	for _, c := range cases {
		env, _, _, err := a.resolveByHost(context.Background(), c.host)
		if err != nil {
			t.Errorf("%s: resolve: %v", c.host, err)
			continue
		}
		if env != c.env {
			t.Errorf("%s: got %s, want %s", c.host, env, c.env)
		}
	}
	for _, miss := range []string{"example.com", "x.y.z.example.com", "a.example.org"} {
		if env, _, _, err := a.resolveByHost(context.Background(), miss); err == nil {
			t.Errorf("%s: expected no match, got %s", miss, env)
		}
	}
}

// The catch-all IngressRoute sends every unmatched host to the activator, so
// an internal service's host (no Ingress of its own) used to resolve here and
// get proxied to its ClusterIP Service from the public internet.
func TestResolveByHost_SkipsInternalAndWorkerEnvs(t *testing.T) {
	t.Parallel()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{kube.GVREnvironments: "KusoEnvironmentList"})
	seed := func(name string, spec map[string]any) {
		u := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "application.kuso.sislelabs.com/v1alpha1",
			"kind":       "KusoEnvironment",
			"metadata":   map[string]any{"name": name, "namespace": "kuso"},
			"spec":       spec,
		}}
		if err := dyn.Tracker().Create(kube.GVREnvironments, u, "kuso"); err != nil {
			t.Fatal(err)
		}
	}
	seed("alpha-db-api-production", map[string]any{"kind": "production", "host": "db-api.alpha.example.com", "internal": true})
	seed("alpha-jobs-production", map[string]any{"kind": "production", "host": "jobs.alpha.example.com", "runtime": "worker"})
	seed("alpha-web-production", map[string]any{"kind": "production", "host": "web.alpha.example.com"})

	a := New(&kube.Client{Dynamic: dyn}, slog.Default())
	for _, host := range []string{"db-api.alpha.example.com", "jobs.alpha.example.com"} {
		if env, _, _, err := a.resolveByHost(context.Background(), host); err == nil {
			t.Errorf("%s resolved to %s; internal/worker envs must not be reachable via the activator", host, env)
		}
	}
	if env, _, _, err := a.resolveByHost(context.Background(), "web.alpha.example.com"); err != nil || env != "alpha-web-production" {
		t.Errorf("public env: got (%q, %v)", env, err)
	}
}

// Every unclaimed host on the internet reaches the activator via the
// catch-all route; a miss must not re-list every env CR per request, and
// the cache keyed by those hosts must stay bounded.
func TestResolveByHost_CachesMissesAndStaysBounded(t *testing.T) {
	t.Parallel()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{kube.GVREnvironments: "KusoEnvironmentList"})
	var lists atomic.Int32
	dyn.PrependReactor("list", "kusoenvironments", func(k8stesting.Action) (bool, runtime.Object, error) {
		lists.Add(1)
		return false, nil, nil
	})
	a := New(&kube.Client{Dynamic: dyn}, slog.Default())
	for i := 0; i < 3; i++ {
		if _, _, _, err := a.resolveByHost(context.Background(), "nothing.example.com"); err == nil {
			t.Fatal("unknown host resolved")
		}
	}
	if got := lists.Load(); got != 1 {
		t.Errorf("3 lookups of one unknown host listed envs %d times, want 1", got)
	}

	for i := 0; i < maxHostCache+50; i++ {
		a.storeHost(fmt.Sprintf("h%d.example.com", i), hostEntry{miss: true, at: time.Now()})
	}
	a.hostMu.RLock()
	n := len(a.hostCache)
	a.hostMu.RUnlock()
	if n > maxHostCache {
		t.Errorf("hostCache grew to %d entries, cap %d", n, maxHostCache)
	}
}

// Past the held-request cap the request is refused instead of held, but
// the wake it asked for still happens.
func TestWakeAndWait_OverHoldCapStillWakes(t *testing.T) {
	const ns, name = "kuso", "app-web-staging"
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{kube.GVREnvironments: "KusoEnvironmentList"})
	env := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "application.kuso.sislelabs.com/v1alpha1",
		"kind":       "KusoEnvironment",
		"metadata": map[string]any{"name": name, "namespace": ns,
			"annotations": map[string]any{"kuso.sislelabs.com/pre-sleep-replicas": "2"}},
		"spec": map[string]any{"replicaCount": int64(0)},
	}}
	if err := dyn.Tracker().Create(kube.GVREnvironments, env, ns); err != nil {
		t.Fatal(err)
	}
	zero := int32(0)
	cs := fake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       appsv1.DeploymentSpec{Replicas: &zero},
	})
	a := New(&kube.Client{Clientset: cs, Dynamic: dyn}, slog.Default())
	a.maxHeld = 1
	a.held.Store(1)

	if err := a.wakeAndWait(context.Background(), ns, name); !errors.Is(err, errHoldCapacity) {
		t.Fatalf("err = %v, want errHoldCapacity", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		d, err := cs.AppsV1().Deployments(ns).Get(context.Background(), name, metav1.GetOptions{})
		if err == nil && *d.Spec.Replicas == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("refused request did not trigger the wake")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if a.held.Load() != 1 {
		t.Errorf("held = %d after refusal, want 1 (refusal must not leak a slot)", a.held.Load())
	}
}
