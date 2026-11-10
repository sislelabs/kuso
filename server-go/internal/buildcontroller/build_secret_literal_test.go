package buildcontroller

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"kuso/server/internal/kube"
)

func envMap(env []corev1.EnvVar) map[string]string {
	m := map[string]string{}
	for _, e := range env {
		m[e.Name] = e.Value
	}
	return m
}

// A DATABASE_URL literal with a password must reach the build without ever
// being a build-arg or ENV line: dockerfile and nixpacks get it as a BuildKit
// secret, static keeps it in its throwaway build pod.
func TestCredentialLiteralRoutedAsBuildSecret(t *testing.T) {
	const dsn = "postgres://u:pw@ep-x.neon.tech/db"
	b := &kube.KusoBuild{Spec: kube.KusoBuildSpec{BuildEnv: map[string]string{
		"DATABASE_URL": dsn,
		"APP_NAME":     "shop",
	}, Image: &kube.KusoImage{Repository: "registry.local/a/b", Tag: "sha"}}}

	for _, strategy := range []string{"dockerfile", "nixpacks"} {
		env := envMap(renderBuildkitContainer(b, strategy, corev1.ResourceRequirements{}).Env)
		if env["KUSO_BUILDENV_KEYS"] != "APP_NAME" {
			t.Errorf("%s: KUSO_BUILDENV_KEYS = %q, want only APP_NAME (build-arg channel)", strategy, env["KUSO_BUILDENV_KEYS"])
		}
		if env["KUSO_BUILDENV_SECRET_KEYS"] != "DATABASE_URL" {
			t.Errorf("%s: KUSO_BUILDENV_SECRET_KEYS = %q, want DATABASE_URL", strategy, env["KUSO_BUILDENV_SECRET_KEYS"])
		}
		if env["KUSO_BE_DATABASE_URL"] != dsn {
			t.Errorf("%s: secret value not provided to buildctl", strategy)
		}
	}

	plan := envMap(renderNixpacksPlanContainer(b).Env)
	if strings.Contains(plan["KUSO_BUILDENV_KEYS"], "DATABASE_URL") {
		t.Error("nixpacks plan would bake DATABASE_URL as --env / ENV")
	}
	if _, ok := plan["KUSO_BE_DATABASE_URL"]; ok {
		t.Error("nixpacks plan needs only the key name, not the value")
	}
	if plan["KUSO_BUILD_SECRET_KEYS"] != "DATABASE_URL" {
		t.Errorf("nixpacks plan KUSO_BUILD_SECRET_KEYS = %q", plan["KUSO_BUILD_SECRET_KEYS"])
	}

	static := envMap(renderStaticPlanContainer(b).Env)
	if static["KUSO_BE_DATABASE_URL"] != dsn {
		t.Error("static build command lost DATABASE_URL")
	}
}

func TestNixpacksSecretMountScript(t *testing.T) {
	requireTools(t, "bash", "awk")
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".nixpacks"), 0o755); err != nil {
		t.Fatal(err)
	}
	df := filepath.Join(dir, ".nixpacks", "Dockerfile")
	in := "FROM base\nENV A=\"1\"\nRUN npm ci\nCOPY . /app\nRUN --mount=type=cache,id=x,target=/c npm run build\n"
	if err := os.WriteFile(df, []byte(in), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", "set -e\n"+nixpacksSecretMountScript)
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"RESERVED=NODE_ENV",
		"KUSO_BUILD_SECRET_KEYS=DATABASE_URL NODE_ENV BAD-KEY SMTP_PASS",
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("script failed: %v\n%s", err, out.String())
	}
	raw, _ := os.ReadFile(df)
	m := "--mount=type=secret,id=DATABASE_URL,env=DATABASE_URL --mount=type=secret,id=SMTP_PASS,env=SMTP_PASS "
	want := "FROM base\nENV A=\"1\"\nRUN " + m + "npm ci\nCOPY . /app\nRUN " + m + "--mount=type=cache,id=x,target=/c npm run build\n"
	if string(raw) != want {
		t.Errorf("Dockerfile =\n%s\nwant\n%s", raw, want)
	}
}
