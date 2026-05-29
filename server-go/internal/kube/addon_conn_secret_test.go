package kube

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// stringDataKeyRe matches an actual `stringData:` YAML key (line-leading,
// whitespace-indented) — NOT the substring inside an explanatory comment
// like "# base64 data: not stringData: …". A comment line starts with '#'
// after the indent, so anchoring on indent-then-key excludes it.
var stringDataKeyRe = regexp.MustCompile(`(?m)^[ \t]*stringData:`)

// addonChartDir resolves the kusoaddon helm chart path relative to this
// file so the test works from any CWD.
func addonChartDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	repoRoot := filepath.Join(filepath.Dir(file), "..", "..", "..")
	return filepath.Join(repoRoot, "operator", "helm-charts", "kusoaddon")
}

// helmTemplateAddon renders the kusoaddon chart for a given addon kind and
// returns combined stdout. Skipped when helm is not on PATH.
func helmTemplateAddon(t *testing.T, kind string, sets ...string) string {
	t.Helper()

	helmBin, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm not found on PATH; skipping addon chart render test")
	}

	args := []string{
		"template", "test-addon", addonChartDir(t),
		"--set", "project=alpha",
		"--set", "name=alpha-" + kind,
		"--set", "kind=" + kind,
	}
	for _, s := range sets {
		args = append(args, "--set", s)
	}

	out, err := exec.Command(helmBin, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm template (kind=%s) failed: %v\n%s", kind, err, out)
	}
	return string(out)
}

// TestAddonConnSecret_UsesBase64Data is the regression anchor for the
// perpetual-helm-churn bug. The connection Secret MUST be rendered with a
// base64 `data:` block, NOT `stringData:`.
//
// Why: the apiserver write-translates `stringData` into base64 `data` and
// nulls `stringData` on the stored object. The operator-sdk helm-operator
// compares its freshly rendered manifest (populated `stringData:`) against
// the stored release (`stringData: null` + `data:`), always sees a diff,
// and runs `helm upgrade` on every 3-minute reconcile — bumping the release
// revision forever and re-applying the addon Service. Each re-apply makes
// kube-router reprogram iptables, opening brief windows where pod→addon
// ClusterIP connections are refused (which breaks migration release Jobs
// and `kuso run`). Rendering as base64 `data:` makes render == stored, so
// the comparison is a true no-op and the churn stops.
func TestAddonConnSecret_UsesBase64Data(t *testing.T) {
	t.Parallel()

	// Every addon kind that renders an in-cluster conn Secret.
	kinds := []string{
		"postgres",
		"redis",
		"nats",
		"s3",
		"clickhouse",
		"meilisearch",
		"mailpit",
	}

	for _, kind := range kinds {
		kind := kind
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			out := helmTemplateAddon(t, kind)

			// The conn Secret (and any other Secret this chart emits)
			// must never use a stringData YAML key — it is the churn
			// trigger. (Matches the key, not the word inside comments.)
			if stringDataKeyRe.MatchString(out) {
				t.Errorf("kind=%s: rendered chart contains a stringData: key — conn Secrets must use base64 data: to avoid perpetual helm-operator reconcile churn.\n%s", kind, out)
			}

			// Sanity: a conn Secret with a data: block is actually present.
			if !strings.Contains(out, "kind: Secret") {
				t.Errorf("kind=%s: expected a Secret in the rendered chart, found none.\n%s", kind, out)
			}
		})
	}
}

// TestAddonConnSecret_HAVariants covers the clustered chart variants, which
// share the same stringData bug.
func TestAddonConnSecret_HAVariants(t *testing.T) {
	t.Parallel()

	kinds := []string{"postgres", "redis", "nats"}
	for _, kind := range kinds {
		kind := kind
		t.Run(kind+"_ha", func(t *testing.T) {
			t.Parallel()
			out := helmTemplateAddon(t, kind, "ha=true")
			if stringDataKeyRe.MatchString(out) {
				t.Errorf("kind=%s ha=true: rendered chart contains a stringData: key — must use base64 data:.\n%s", kind, out)
			}
		})
	}
}
