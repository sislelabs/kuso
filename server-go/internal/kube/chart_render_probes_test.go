package kube

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A Recreate env (RWO volume) has no overlap between old and new pod, so the
// preStop sleep only adds downtime; skip it there.
func TestKusoEnvironmentChart_PreStopSkippedOnRecreate(t *testing.T) {
	t.Parallel()
	out := helmTemplate(t, "test-env", "volumes[0].name=data", "volumes[0].mountPath=/data")
	if !strings.Contains(out, "type: Recreate") {
		t.Fatalf("fixture should render a Recreate strategy:\n%s", out)
	}
	if strings.Contains(out, "preStop:") {
		t.Errorf("Recreate env should not render a preStop sleep:\n%s", out)
	}
}

// The sleep handler needs Kubernetes >= 1.30; older apiservers drop the
// field and reject the resulting empty preStop, so the ReplicaSet can't
// create pods.
func TestKusoEnvironmentChart_PreStopNeedsKube130(t *testing.T) {
	t.Parallel()
	helmBin, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm not found on PATH; skipping chart render test")
	}
	render := func(kubeVersion string) string {
		out, err := exec.Command(helmBin, "template", "test-env", chartDir(t),
			"--set", "project=alpha", "--set", "service=web",
			"--set", "image.repository=registry.local/alpha/web", "--set", "image.tag=sha123",
			"--show-only", "templates/deployment.yaml",
			"--kube-version", kubeVersion).CombinedOutput()
		if err != nil {
			t.Fatalf("helm template --kube-version %s: %v\n%s", kubeVersion, err, out)
		}
		return string(out)
	}
	if out := render("v1.29.4+k3s1"); strings.Contains(out, "preStop:") {
		t.Errorf("1.29 apiserver should not get a preStop sleep:\n%s", out)
	}
	if out := render("v1.30.0+k3s1"); !strings.Contains(out, "preStop:") {
		t.Errorf("1.30 apiserver should get the preStop sleep:\n%s", out)
	}
}

// The CRD prunes a non-secretKeyRef valueFrom to `valueFrom: {}`, which the
// apiserver rejects for the whole Deployment. The chart drops such entries
// and keeps the rest unchanged.
func TestKusoEnvironmentChart_DropsEmptyValueFrom(t *testing.T) {
	t.Parallel()
	helmBin, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm not found on PATH; skipping chart render test")
	}
	vals := filepath.Join(t.TempDir(), "values.yaml")
	body := `project: alpha
service: web
image: {repository: registry.local/alpha/web, tag: sha123}
envVars:
  - {name: PLAIN, value: "1"}
  - name: FROM_SECRET
    valueFrom: {secretKeyRef: {name: alpha-db-conn, key: URL}}
  - {name: PRUNED, valueFrom: {}}
`
	if err := os.WriteFile(vals, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(helmBin, "template", "test-env", chartDir(t), "-f", vals,
		"--show-only", "templates/deployment.yaml").CombinedOutput()
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, out)
	}
	s := string(out)
	if strings.Contains(s, "PRUNED") || strings.Contains(s, "valueFrom: {}") {
		t.Errorf("empty valueFrom entry should be dropped:\n%s", s)
	}
	for _, want := range []string{"name: PLAIN", "name: FROM_SECRET", "name: alpha-db-conn"} {
		if strings.Count(s, want) != 2 { // app container + release-hook/init copy
			t.Errorf("want %q twice (both env blocks), got %d:\n%s", want, strings.Count(s, want), s)
		}
	}
}

// initialDelaySeconds 0 is valid and must survive the render; `default 5`
// used to turn it into 5.
func TestKusoEnvironmentChart_HealthcheckZeroDelay(t *testing.T) {
	t.Parallel()
	zero := helmTemplate(t, "test-env", "healthcheck.path=/healthz", "healthcheck.initialDelaySeconds=0")
	if strings.Count(zero, "initialDelaySeconds: 0") != 2 {
		t.Errorf("explicit 0 delay not rendered on both HTTP probes:\n%s", zero)
	}
	unset := helmTemplate(t, "test-env", "healthcheck.path=/healthz")
	if strings.Count(unset, "initialDelaySeconds: 5") != 2 {
		t.Errorf("unset delay should default to 5 on both HTTP probes:\n%s", unset)
	}
}
