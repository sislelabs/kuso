package kube

import (
	"os/exec"
	"strings"
	"testing"
)

// helmTemplateEnvNS renders the whole kusoenvironment chart into a
// namespace (the helper in chart_render_test.go pins deployment.yaml and
// the default namespace, both of which hide this behaviour).
func helmTemplateEnvNS(t *testing.T, ns string, sets ...string) string {
	t.Helper()
	helmBin, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm not found on PATH; skipping chart render test")
	}
	args := []string{
		"template", "test-env", chartDir(t), "-n", ns,
		"--set", "project=alpha",
		"--set", "service=web",
		"--set", "image.repository=registry.local/alpha/web",
		"--set", "image.tag=sha123",
		"--set", "host=web.example.com",
	}
	for _, s := range sets {
		args = append(args, "--set", s)
	}
	out, err := exec.Command(helmBin, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, out)
	}
	return string(out)
}

// TestKusoEnvironmentChart_ActivatorBackendNamespace: an Ingress backend
// must live in the Ingress's namespace, and kuso-activator only exists in
// `kuso`. Sleep/stop envs in a custom project namespace used to route to a
// Service that didn't exist there (503, never woke).
func TestKusoEnvironmentChart_ActivatorBackendNamespace(t *testing.T) {
	t.Parallel()
	const externalName = "externalName: kuso-activator.kuso.svc.cluster.local"
	cases := []struct {
		name         string
		ns           string
		sets         []string
		wantBackend  string
		wantMirrorSv bool
	}{
		{"home_ns_sleep_uses_shared_activator", "kuso", []string{"sleep.enabled=true"}, "name: kuso-activator", false},
		{"custom_ns_sleep_uses_mirror", "kuso-alpha", []string{"sleep.enabled=true"}, "name: test-env-activator", true},
		{"custom_ns_stopped_uses_mirror", "kuso-alpha", []string{"stopped=true"}, "name: test-env-activator", true},
		{"custom_ns_awake_routes_to_app", "kuso-alpha", nil, "name: test-env\n", false},
		{"custom_ns_worker_no_mirror", "kuso-alpha", []string{"sleep.enabled=true", "runtime=worker"}, "", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := helmTemplateEnvNS(t, tc.ns, tc.sets...)
			if tc.wantBackend != "" && !strings.Contains(out, tc.wantBackend) {
				t.Errorf("expected ingress backend %q, got:\n%s", tc.wantBackend, out)
			}
			if got := strings.Contains(out, externalName); got != tc.wantMirrorSv {
				t.Errorf("ExternalName activator mirror present=%v, want %v:\n%s", got, tc.wantMirrorSv, out)
			}
		})
	}
}
