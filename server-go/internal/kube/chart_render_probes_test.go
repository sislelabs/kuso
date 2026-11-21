package kube

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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

// appProbes decodes the app container's readiness/liveness probes.
func appProbes(t *testing.T, rendered string) (ready, live map[string]any) {
	t.Helper()
	var d struct {
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Name           string         `yaml:"name"`
						ReadinessProbe map[string]any `yaml:"readinessProbe"`
						LivenessProbe  map[string]any `yaml:"livenessProbe"`
					} `yaml:"containers"`
				} `yaml:"spec"`
			} `yaml:"template"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal([]byte(rendered), &d); err != nil {
		t.Fatalf("decode deployment: %v\n%s", err, rendered)
	}
	for _, c := range d.Spec.Template.Spec.Containers {
		if c.Name == "app" {
			return c.ReadinessProbe, c.LivenessProbe
		}
	}
	t.Fatalf("no app container:\n%s", rendered)
	return nil, nil
}

// Readiness can't run before the TCP startupProbe passes, so its initial
// delay and period are pure time-to-Ready on every start and wake: they
// default to 0s/2s. Liveness keeps 5s/10s. Explicit values win on both, 0
// included (`default 5` used to turn an explicit 0 into 5). The defaulted
// readiness threshold scales to keep the old 30s before a pod is unready.
func TestKusoEnvironmentChart_HealthcheckProbeTiming(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                  string
		sets                  []string
		readyDelay, readyPer  int
		liveDelay, livePeriod int
		readyFail             int
	}{
		{"defaults", nil, 0, 2, 5, 10, 15},
		{"explicit zero delay", []string{"healthcheck.initialDelaySeconds=0"}, 0, 2, 0, 10, 15},
		{"explicit values", []string{"healthcheck.initialDelaySeconds=7", "healthcheck.periodSeconds=4"}, 7, 4, 7, 4, 3},
		{"explicit threshold", []string{"healthcheck.failureThreshold=5"}, 0, 2, 5, 10, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ready, live := appProbes(t, helmTemplate(t, "test-env", append([]string{"healthcheck.path=/healthz"}, tc.sets...)...))
			if ready["httpGet"] == nil || live["httpGet"] == nil {
				t.Fatalf("want HTTP probes, got readiness=%v liveness=%v", ready, live)
			}
			check := func(probe string, m map[string]any, delay, period int) {
				if m["initialDelaySeconds"] != delay || m["periodSeconds"] != period {
					t.Errorf("%s: initialDelaySeconds=%v periodSeconds=%v, want %d/%d", probe, m["initialDelaySeconds"], m["periodSeconds"], delay, period)
				}
			}
			check("readiness", ready, tc.readyDelay, tc.readyPer)
			check("liveness", live, tc.liveDelay, tc.livePeriod)
			if ready["failureThreshold"] != tc.readyFail {
				t.Errorf("readiness failureThreshold=%v, want %d", ready["failureThreshold"], tc.readyFail)
			}
		})
	}
}
