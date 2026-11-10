package buildcontroller

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"kuso/server/internal/kube"
)

// publicEnv names ride the BuildArgs channel with the __KUSO_RUNTIME_<KEY>__
// sentinel as the value, overriding a BuildArgs entry for the same key (the
// old kusobuild chart appended them after buildArgs, so the later --opt won).
func TestPublicEnvBakedAsSentinelBuildArgs(t *testing.T) {
	b := &kube.KusoBuild{Spec: kube.KusoBuildSpec{
		BuildArgs: map[string]string{"APP_VERSION": "1.4.2", "NEXT_PUBLIC_SITE_URL": "https://baked.example"},
		PublicEnv: []string{"NEXT_PUBLIC_API_URL", "NEXT_PUBLIC_SITE_URL", "BAD-KEY", "BAD$(x)"},
	}}
	got := envMap(buildArgsContainerVars(b))
	want := map[string]string{
		"KUSO_BA_APP_VERSION":          "1.4.2",
		"KUSO_BA_NEXT_PUBLIC_API_URL":  "__KUSO_RUNTIME_NEXT_PUBLIC_API_URL__",
		"KUSO_BA_NEXT_PUBLIC_SITE_URL": "__KUSO_RUNTIME_NEXT_PUBLIC_SITE_URL__",
		"KUSO_BUILDARG_KEYS":           "APP_VERSION NEXT_PUBLIC_API_URL NEXT_PUBLIC_SITE_URL",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("unexpected vars (invalid publicEnv keys must be dropped): %v", got)
	}
}

func TestPublicEnvOnlyStillReachesEveryStrategy(t *testing.T) {
	b := &kube.KusoBuild{Spec: kube.KusoBuildSpec{
		PublicEnv: []string{"VITE_API_URL"},
		Image:     &kube.KusoImage{Repository: "registry.local/alpha/api", Tag: "sha"},
		Static:    &kube.KusoStaticSpec{OutputDir: "dist"},
	}}
	const want = "__KUSO_RUNTIME_VITE_API_URL__"
	for name, envs := range map[string][]corev1.EnvVar{
		"dockerfile buildkit": renderBuildkitContainer(b, "dockerfile", corev1.ResourceRequirements{}).Env,
		"nixpacks-plan":       renderNixpacksPlanContainer(b).Env,
		"static-plan":         renderStaticPlanContainer(b).Env,
	} {
		m := envMap(envs)
		if m["KUSO_BA_VITE_API_URL"] != want || m["KUSO_BUILDARG_KEYS"] != "VITE_API_URL" {
			t.Errorf("%s: publicEnv sentinel missing: KUSO_BA_VITE_API_URL=%q KUSO_BUILDARG_KEYS=%q",
				name, m["KUSO_BA_VITE_API_URL"], m["KUSO_BUILDARG_KEYS"])
		}
	}
}

// The publicEnv key is usually also a literal runtime env var, so it lands in
// BuildEnv with the real value. The build must still see the sentinel, or the
// first environment's value is inlined into every environment's bundle.
func TestStaticBuildSeesPublicEnvSentinelOverBuildEnv(t *testing.T) {
	requireTools(t, "sh")
	b := &kube.KusoBuild{Spec: kube.KusoBuildSpec{
		Strategy: "static",
		Static: &kube.KusoStaticSpec{
			BuildCmd:  `mkdir -p dist && printf '%s' "$VITE_API_URL" > dist/env.txt`,
			OutputDir: "dist",
		},
		BuildEnv:  map[string]string{"VITE_API_URL": "https://prod.example"},
		PublicEnv: []string{"VITE_API_URL"},
	}}
	c := renderStaticPlanContainer(b)
	dir := t.TempDir()
	cmd := exec.Command("sh", "-c", c.Args[0])
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	for _, e := range c.Env {
		cmd.Env = append(cmd.Env, e.Name+"="+e.Value)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	got, err := os.ReadFile(filepath.Join(dir, "dist", "env.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "__KUSO_RUNTIME_VITE_API_URL__"; string(got) != want {
		t.Errorf("build saw %q, want %q", got, want)
	}
}
