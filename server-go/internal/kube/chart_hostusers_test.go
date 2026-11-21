package kube

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCharts_HostUsers pins the opt-in user namespace on every chart that
// runs the service image: only an explicit hostUsers=false renders the pod
// field, and unset/true/null leave the pod spec byte-identical to a render
// without the key, so the upgrade rolls no existing pod.
func TestCharts_HostUsers(t *testing.T) {
	t.Parallel()
	helmBin, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm not found on PATH; skipping chart render test")
	}
	charts := filepath.Dir(chartDir(t))
	render := func(t *testing.T, chart string, sets ...string) string {
		t.Helper()
		args := []string{"template", "r", filepath.Join(charts, chart),
			"--set", "project=alpha", "--set", "service=web",
			"--set", "image.repository=registry.local/alpha/web", "--set", "image.tag=sha123",
			"--set", "schedule=*/5 * * * *",
		}
		for _, s := range sets {
			args = append(args, "--set", s)
		}
		out, err := exec.Command(helmBin, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("helm template %s %v: %v\n%s", chart, sets, err, out)
		}
		return string(out)
	}
	for _, chart := range []string{"kusoenvironment", "kusocron", "kusorun"} {
		t.Run(chart, func(t *testing.T) {
			t.Parallel()
			base := render(t, chart)
			if strings.Contains(base, "hostUsers") {
				t.Fatalf("default render carries hostUsers:\n%s", base)
			}
			for _, set := range []string{"securityContext.hostUsers=true", "securityContext=null", "securityContext.capabilities.add={SETUID}"} {
				out := render(t, chart, set)
				if strings.Contains(out, "hostUsers") {
					t.Errorf("%s rendered hostUsers:\n%s", set, out)
				}
			}
			if got := render(t, chart, "securityContext.hostUsers=true"); got != base {
				t.Errorf("hostUsers=true changed the render vs unset")
			}
			on := render(t, chart, "securityContext.hostUsers=false")
			if strings.Count(on, "hostUsers: false") != 1 {
				t.Fatalf("hostUsers=false: want exactly one pod-level hostUsers: false\n%s", on)
			}
			if strings.ReplaceAll(on, "\n"+podIndent(chart)+"hostUsers: false", "") != base {
				t.Errorf("hostUsers=false changed more than the one pod-spec line")
			}
		})
	}
}

// podIndent is the pod spec's field indent in each chart; the CronJob's
// pod spec sits two levels deeper under jobTemplate.
func podIndent(chart string) string {
	if chart == "kusocron" {
		return strings.Repeat(" ", 10)
	}
	return strings.Repeat(" ", 6)
}
