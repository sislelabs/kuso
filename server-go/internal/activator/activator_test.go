package activator

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"

	"kuso/server/internal/kube"
)

func TestHostOnly(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"app.example.com":      "app.example.com",
		"app.example.com:8080": "app.example.com",
		"":                     "",
		"localhost:3000":       "localhost",
	}
	for in, want := range cases {
		if got := hostOnly(in); got != want {
			t.Errorf("hostOnly(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHostMatches(t *testing.T) {
	t.Parallel()
	env := &kube.KusoEnvironment{}
	env.Spec.Host = "Primary.Example.com"
	env.Spec.AdditionalHosts = []string{"www.example.com", "alt.example.org"}

	yes := []string{"primary.example.com", "PRIMARY.example.com", "www.example.com", "alt.example.org"}
	for _, h := range yes {
		if !hostMatches(h, env) {
			t.Errorf("hostMatches(%q) = false, want true", h)
		}
	}
	no := []string{"other.example.com", "example.com", ""}
	for _, h := range no {
		if hostMatches(h, env) {
			t.Errorf("hostMatches(%q) = true, want false", h)
		}
	}
}

func TestReadyReplicas(t *testing.T) {
	t.Parallel()
	if got := readyReplicas(nil); got != 0 {
		t.Errorf("nil deployment: got %d, want 0", got)
	}
	dep := &appsv1.Deployment{}
	dep.Status.ReadyReplicas = 3
	if got := readyReplicas(dep); got != 3 {
		t.Errorf("got %d, want 3", got)
	}
}
