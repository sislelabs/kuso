package updater

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func entrypointFunc(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "build", "updater", "entrypoint.sh"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?ms)^` + name + `\(\) \{.*?^\}`).Find(raw)
	if m == nil {
		t.Fatalf("%s not found in entrypoint.sh", name)
	}
	return string(m)
}

func runBash(t *testing.T, script string) string {
	t.Helper()
	for _, bin := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not on PATH")
		}
	}
	out, err := exec.Command("bash", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// RUN-9: the server rollout wait follows the Deployment's startupProbe
// budget (migrations run inside it) instead of a fixed 180s.
func TestEntrypointServerStartupBudget(t *testing.T) {
	fn := entrypointFunc(t, "server_startup_budget")
	stub := `kubectl() { case "$*" in *periodSeconds*) echo 5;; *failureThreshold*) echo 60;; esac; }` + "\n"
	if got := runBash(t, "set -eu\nNS=kuso\n"+stub+fn+"\nserver_startup_budget\n"); got != "300" {
		t.Errorf("budget = %q, want 300", got)
	}
	if got := runBash(t, "set -eu\nNS=kuso\nkubectl() { return 1; }\n"+fn+"\nserver_startup_budget\n"); got != "300" {
		t.Errorf("unreadable probe: budget = %q, want the 300 default", got)
	}
}

// RUN-10: failing_releases names CRs whose helm release is failing and
// nothing else, so the post-roll check can diff against a baseline.
func TestEntrypointFailingReleases(t *testing.T) {
	fn := entrypointFunc(t, "failing_releases")
	list := `{"items":[
{"kind":"KusoAddon","metadata":{"namespace":"p","name":"pg"},"status":{"conditions":[{"type":"ReleaseFailed","status":"True"}]}},
{"kind":"KusoEnvironment","metadata":{"namespace":"p","name":"web"},"status":{"conditions":[{"type":"Deployed","status":"True"},{"type":"ReleaseFailed","status":"False"}]}},
{"kind":"KusoEnvironment","metadata":{"namespace":"p","name":"api"},"status":{"conditions":[{"type":"Irreconcilable","status":"True"}]}},
{"kind":"KusoEnvironment","metadata":{"namespace":"p","name":"new"}}]}`
	got := runBash(t, "set -eu\nkubectl() { cat <<'J'\n"+list+"\nJ\n}\n"+fn+"\nfailing_releases\n")
	want := "KusoAddon/p/pg\nKusoEnvironment/p/api"
	if got != want {
		t.Errorf("failing_releases =\n%s\nwant\n%s", got, want)
	}
	out := runBash(t, "set -u\nkubectl() { return 1; }\n"+fn+"\nfailing_releases || echo UNREADABLE\n")
	if out != "UNREADABLE" {
		t.Errorf("a failed read must be distinguishable from none failing, got %q", out)
	}
}
