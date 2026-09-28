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
