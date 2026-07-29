package kube

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// kusoCronChartDir resolves the kusocron chart relative to this file so
// the tests work from any CWD.
func kusoCronChartDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	repoRoot := filepath.Join(filepath.Dir(file), "..", "..", "..")
	return filepath.Join(repoRoot, "operator", "helm-charts", "kusocron")
}

// helmTemplateCron renders the kusocron chart with the given --set pairs.
// Skips if helm is not on PATH.
func helmTemplateCron(t *testing.T, sets ...string) string {
	t.Helper()
	helmBin, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm not found on PATH; skipping chart render test")
	}
	args := []string{
		"template", "test-cron", kusoCronChartDir(t),
		"--set", "project=alpha",
		"--set", "schedule=* * * * *",
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

// TestKusoCronChart_SuspendGuard verifies MED-1a: a kind=http cron has no
// image by design (it hardcodes curlimages/curl) and must NOT be pinned
// suspended by the no-image guard, whereas a service-kind cron with no
// built image yet SHOULD be suspended until its first build.
func TestKusoCronChart_SuspendGuard(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		sets        []string
		wantSuspend string
	}{
		{
			name:        "http_no_image_not_suspended",
			sets:        []string{"kind=http", "url=https://example.com/health"},
			wantSuspend: "suspend: false",
		},
		{
			name:        "http_user_suspend_honored",
			sets:        []string{"kind=http", "url=https://example.com/health", "suspend=true"},
			wantSuspend: "suspend: true",
		},
		{
			name:        "service_no_image_suspended",
			sets:        []string{"kind=service", "service=web"},
			wantSuspend: "suspend: true",
		},
		{
			name:        "service_with_image_honors_user_suspend",
			sets:        []string{"kind=service", "service=web", "image.repository=registry.local/alpha/web"},
			wantSuspend: "suspend: false",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := helmTemplateCron(t, tc.sets...)
			if !strings.Contains(out, tc.wantSuspend) {
				t.Errorf("expected render to contain %q, got:\n%s", tc.wantSuspend, out)
			}
		})
	}
}

// TestKusoCronChart_SecretRefOptional verifies MED-1c: envFromSecrets
// mounts carry optional:true so a deleted addon's missing conn secret
// doesn't pin every cron fire in CreateContainerConfigError.
func TestKusoCronChart_SecretRefOptional(t *testing.T) {
	t.Parallel()
	out := helmTemplateCron(t,
		"kind=service", "service=web",
		"image.repository=registry.local/alpha/web",
		"envFromSecrets[0]=alpha-pg-conn",
	)
	if !strings.Contains(out, "secretRef:") {
		t.Fatalf("expected a secretRef mount, none rendered:\n%s", out)
	}
	if !strings.Contains(out, "optional: true") {
		t.Errorf("expected secretRef to carry optional: true, got:\n%s", out)
	}
}

// TestKusoCronChart_StartingDeadline verifies MED-1d: the chart renders
// startingDeadlineSeconds (defaulting to 300) so a fallen-behind CronJob
// self-heals, and omits it when explicitly set to 0.
func TestKusoCronChart_StartingDeadline(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		sets       []string
		wantSubstr string // "" => expect the field absent
	}{
		{
			name:       "default_300",
			sets:       []string{"kind=http", "url=https://example.com/h"},
			wantSubstr: "startingDeadlineSeconds: 300",
		},
		{
			name:       "custom_600",
			sets:       []string{"kind=http", "url=https://example.com/h", "startingDeadlineSeconds=600"},
			wantSubstr: "startingDeadlineSeconds: 600",
		},
		{
			name:       "zero_omits_field",
			sets:       []string{"kind=http", "url=https://example.com/h", "startingDeadlineSeconds=0"},
			wantSubstr: "",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := helmTemplateCron(t, tc.sets...)
			has := strings.Contains(out, "startingDeadlineSeconds:")
			if tc.wantSubstr == "" {
				if has {
					t.Errorf("expected NO startingDeadlineSeconds when set to 0, got:\n%s", out)
				}
				return
			}
			if !strings.Contains(out, tc.wantSubstr) {
				t.Errorf("expected render to contain %q, got:\n%s", tc.wantSubstr, out)
			}
		})
	}
}
