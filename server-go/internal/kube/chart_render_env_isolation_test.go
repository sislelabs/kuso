package kube

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func renderChart(t *testing.T, chart, release string, args ...string) string {
	t.Helper()
	helmBin, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm not found on PATH; skipping chart render test")
	}
	dir := filepath.Join(filepath.Dir(chartDir(t)), chart)
	out, err := exec.Command(helmBin, append([]string{"template", release, dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm template %s: %v\n%s", chart, err, out)
	}
	return string(out)
}

var connKeyRE = regexp.MustCompile(`conn\.kuso\.sislelabs\.com/[0-9a-f]{20}`)

// The env pod's subscription label and the addon's NetworkPolicy peer are
// computed in two charts; if they drift, enabling isolation cuts every app
// off from its database.
func TestEnvIsolation_SubscriberKeyMatchesAcrossCharts(t *testing.T) {
	t.Parallel()
	env := renderChart(t, "kusoenvironment", "p-web-production",
		"--show-only", "templates/deployment.yaml",
		"--set", "project=p", "--set", "service=p-web",
		"--set", "image.repository=r/p/web", "--set", "image.tag=1",
		"--set", "envScope=production",
		"--set", "envFromSecrets={p-shared,p-db-conn}",
	)
	addon := renderChart(t, "kusoaddon", "p-db",
		"--show-only", "templates/networkpolicy.yaml",
		"--set", "project=p", "--set", "kind=postgres",
	)
	envKeys := connKeyRE.FindAllString(env, -1)
	addonKeys := connKeyRE.FindAllString(addon, -1)
	if len(envKeys) != 1 || len(addonKeys) != 1 {
		t.Fatalf("want one conn key each, env=%v addon=%v\n%s", envKeys, addonKeys, env)
	}
	if envKeys[0] != addonKeys[0] {
		t.Fatalf("env pod key %s != addon policy key %s", envKeys[0], addonKeys[0])
	}
	if !strings.Contains(env, `kuso.sislelabs.com/env: "production"`) {
		t.Fatalf("env label missing:\n%s", env)
	}
}

// An env CR the server hasn't stamped yet must render no new pod labels,
// so the upgrade itself rolls no pods.
func TestEnvIsolation_UnstampedEnvGetsNoLabels(t *testing.T) {
	t.Parallel()
	env := renderChart(t, "kusoenvironment", "p-web-production",
		"--show-only", "templates/deployment.yaml",
		"--set", "project=p", "--set", "service=p-web",
		"--set", "image.repository=r/p/web", "--set", "image.tag=1",
		"--set", "envFromSecrets={p-db-conn}",
	)
	if strings.Contains(env, "kuso.sislelabs.com/env:") || connKeyRE.MatchString(env) {
		t.Fatalf("unstamped env rendered isolation labels:\n%s", env)
	}
}

func TestEnvIsolation_ProjectGate(t *testing.T) {
	t.Parallel()
	off := renderChart(t, "kusoproject", "p", "--show-only", "templates/networkpolicy.yaml")
	on := renderChart(t, "kusoproject", "p", "--show-only", "templates/networkpolicy.yaml",
		"--set", "networkPolicy.envIsolation=true")
	for _, name := range []string{"p-allow-intra-env-production", "p-allow-addon-legacy", "p-allow-intra-project-egress"} {
		if strings.Contains(off, "name: "+name+"\n") {
			t.Errorf("%s rendered with envIsolation off", name)
		}
		if !strings.Contains(on, "name: "+name+"\n") {
			t.Errorf("%s missing with envIsolation on", name)
		}
	}
	if !strings.Contains(on, `key: kuso.sislelabs.com/env, operator: NotIn, values: ["production"]`) {
		t.Error("allow-intra-project still admits into production pods with envIsolation on")
	}
	if !strings.Contains(off, "name: p-allow-instance-db-egress") {
		t.Error("legacy instance-db egress must stay by default")
	}
	noIDB := renderChart(t, "kusoproject", "p", "--show-only", "templates/networkpolicy.yaml",
		"--set", "networkPolicy.instanceDBEgress=false")
	if strings.Contains(noIDB, "name: p-allow-instance-db-egress") {
		t.Error("instanceDBEgress=false still renders the legacy rule")
	}
}

// Credential-less addons admit subscribers and their own pods only;
// password-protected ones also admit production pods.
func TestEnvIsolation_CredentiallessAddonsAdmitSubscribersOnly(t *testing.T) {
	t.Parallel()
	for kind, wantProd := range map[string]bool{"redpanda": false, "mailpit": false, "postgres": true, "redis": true} {
		out := renderChart(t, "kusoaddon", "p-x", "--show-only", "templates/networkpolicy.yaml",
			"--set", "project=p", "--set", "kind="+kind)
		if got := strings.Contains(out, "kuso.sislelabs.com/env: production"); got != wantProd {
			t.Errorf("%s: admits env=production = %v, want %v", kind, got, wantProd)
		}
	}
}
