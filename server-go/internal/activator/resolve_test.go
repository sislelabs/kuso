package activator

import (
	"context"
	"log/slog"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

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
