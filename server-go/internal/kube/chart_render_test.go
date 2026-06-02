package kube

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// chartDir returns the absolute path to the kusoenvironment helm chart,
// resolved relative to this file so the tests work from any CWD.
func chartDir(t *testing.T) string {
	t.Helper()
	// __file__ → server-go/internal/kube/chart_render_test.go
	// chart lives at  <repo-root>/operator/helm-charts/kusoenvironment
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	// file = …/server-go/internal/kube/chart_render_test.go
	// Go up 3 dirs (kube → internal → server-go) to reach repo root,
	// then descend into the chart.
	repoRoot := filepath.Join(filepath.Dir(file), "..", "..", "..")
	return filepath.Join(repoRoot, "operator", "helm-charts", "kusoenvironment")
}

// helmTemplate runs `helm template` with the given extra --set pairs and
// returns the combined stdout. The test is skipped if helm is not on PATH.
func helmTemplate(t *testing.T, releaseName string, sets ...string) string {
	t.Helper()

	helmBin, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm not found on PATH; skipping chart render test")
	}

	args := []string{
		"template", releaseName, chartDir(t),
		// minimal required values so the chart renders without errors
		"--set", "project=alpha",
		"--set", "service=web",
		"--set", "image.repository=registry.local/alpha/web",
		"--set", "image.tag=sha123",
		// deployment.yaml only — we only care about the pod template labels
		"--show-only", "templates/deployment.yaml",
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

// TestKusoEnvironmentChart_EgressLabel verifies the pod template label
// kuso.sislelabs.com/network-egress-public is present by default and
// absent when privateEgress is set to true.
//
// This covers the change introduced in Task 5 of the public-egress-fix
// branch: the deployment template stamps the label so the kusoproject
// NetworkPolicy's allow-public-egress rule can select the right pods.
func TestKusoEnvironmentChart_EgressLabel(t *testing.T) {
	t.Parallel()

	const egressLabel = `kuso.sislelabs.com/network-egress-public: "true"`

	cases := []struct {
		name        string
		sets        []string
		wantPresent bool
	}{
		{
			name:        "default_values_pod_has_egress_label",
			sets:        nil,
			wantPresent: true,
		},
		{
			name:        "privateEgress_true_pod_has_no_egress_label",
			sets:        []string{"privateEgress=true"},
			wantPresent: false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := helmTemplate(t, "test-env", tc.sets...)
			got := strings.Contains(out, egressLabel)
			if got != tc.wantPresent {
				if tc.wantPresent {
					t.Errorf("expected pod template to contain %q, but it did not.\nRendered output:\n%s", egressLabel, out)
				} else {
					t.Errorf("expected pod template NOT to contain %q, but it did.\nRendered output:\n%s", egressLabel, out)
				}
			}
		})
	}
}

// TestKusoEnvironmentChart_SpreadPolicy verifies the topology-spread
// constraint's whenUnsatisfiable is driven by spec.spreadPolicy:
// "hard" → DoNotSchedule (replicas forced onto distinct nodes), unset/
// "soft" → ScheduleAnyway, and the block is absent for a single replica.
func TestKusoEnvironmentChart_SpreadPolicy(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		sets       []string
		wantSubstr string // "" means: expect NO topologySpreadConstraints block
	}{
		{
			name:       "replicas2_hard_DoNotSchedule",
			sets:       []string{"replicaCount=2", "spreadPolicy=hard"},
			wantSubstr: "whenUnsatisfiable: DoNotSchedule",
		},
		{
			name:       "replicas2_unset_ScheduleAnyway",
			sets:       []string{"replicaCount=2"},
			wantSubstr: "whenUnsatisfiable: ScheduleAnyway",
		},
		{
			name:       "replicas2_soft_ScheduleAnyway",
			sets:       []string{"replicaCount=2", "spreadPolicy=soft"},
			wantSubstr: "whenUnsatisfiable: ScheduleAnyway",
		},
		{
			name:       "replicas1_no_spread_block",
			sets:       []string{"replicaCount=1", "spreadPolicy=hard"},
			wantSubstr: "", // single replica → no constraint at all
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := helmTemplate(t, "test-env", tc.sets...)
			hasBlock := strings.Contains(out, "topologySpreadConstraints:")
			if tc.wantSubstr == "" {
				if hasBlock {
					t.Errorf("expected NO topologySpreadConstraints for single replica, got:\n%s", out)
				}
				return
			}
			if !hasBlock {
				t.Fatalf("expected a topologySpreadConstraints block, none rendered:\n%s", out)
			}
			if !strings.Contains(out, tc.wantSubstr) {
				t.Errorf("expected render to contain %q, got:\n%s", tc.wantSubstr, out)
			}
		})
	}
}
