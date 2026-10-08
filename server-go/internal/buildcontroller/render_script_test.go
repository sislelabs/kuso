package buildcontroller

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"kuso/server/internal/kube"
)

func requireTools(t *testing.T, tools ...string) {
	t.Helper()
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH", tool)
		}
	}
}

// unquoteDockerfileWord reverses the escaping a Dockerfile double-quoted
// word applies: a backslash before \, " or $ yields that character.
func unquoteDockerfileWord(t *testing.T, s string) string {
	t.Helper()
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		t.Fatalf("value %q is not double-quoted", s)
	}
	s = s[1 : len(s)-1]
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+1 < len(s) && strings.IndexByte(`\"$`, s[i+1]) >= 0 {
			i++
			c = s[i]
		} else if c == '"' || c == '$' || c == '\\' {
			t.Fatalf("unescaped %q in %q", c, s)
		}
		b.WriteByte(c)
	}
	return b.String()
}

func TestNixpacksEnvInjectScriptPreservesValues(t *testing.T) {
	requireTools(t, "bash", "awk", "sed", "grep")
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".nixpacks"), 0o755); err != nil {
		t.Fatal(err)
	}
	dockerfile := filepath.Join(dir, ".nixpacks", "Dockerfile")
	if err := os.WriteFile(dockerfile, []byte("FROM base\nRUN echo hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"GOFLAGS": "-mod=mod",
		"WIN":     `C:\temp\new`,
		"EMPTY":   "",
		"JSON":    `{"a":"$HOME"}`,
		"TRAIL":   `ends in backslash\`,
		"APP":     `say "hi" $(id) ${X}`,
	}
	cmd := exec.Command("bash", "-c", "set -e\n"+nixpacksEnvInjectScript)
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"RESERVED=NODE_ENV",
		"EXTRA_ENVS=GOFLAGS=-mod=mod",
		"KUSO_BUILDENV_KEYS=WIN EMPTY JSON TRAIL NL NODE_ENV",
		"KUSO_BE_WIN=" + want["WIN"],
		"KUSO_BE_EMPTY=",
		"KUSO_BE_JSON=" + want["JSON"],
		"KUSO_BE_TRAIL=" + want["TRAIL"],
		"KUSO_BE_NL=line1\nline2",
		"KUSO_BE_NODE_ENV=production",
		"KUSO_BUILDARG_KEYS=APP",
		"KUSO_BA_APP=" + want["APP"],
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("script failed: %v\n%s", err, out.String())
	}
	raw, err := os.ReadFile(dockerfile)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if lines[0] != "FROM base" || lines[len(lines)-1] != "RUN echo hi" {
		t.Fatalf("ENV lines not inserted right after FROM:\n%s", raw)
	}
	got := map[string]string{}
	var order []string
	for _, l := range lines[1 : len(lines)-1] {
		rest, ok := strings.CutPrefix(l, "ENV ")
		if !ok {
			t.Fatalf("unexpected line %q", l)
		}
		k, v, ok := strings.Cut(rest, "=")
		if !ok {
			t.Fatalf("ENV line without =: %q", l)
		}
		got[k] = unquoteDockerfileWord(t, v)
		order = append(order, k)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("ENV %s = %q, want %q", k, got[k], v)
		}
	}
	if _, ok := got["NL"]; ok {
		t.Error("multi-line value produced an ENV line")
	}
	if _, ok := got["NODE_ENV"]; ok {
		t.Error("reserved key produced an ENV line")
	}
	if strings.Join(order, " ") != "GOFLAGS WIN EMPTY JSON TRAIL APP" {
		t.Errorf("ENV order = %v", order)
	}
	log := out.String()
	if !strings.Contains(log, "ENV NL not baked") {
		t.Errorf("skip of multi-line value not logged:\n%s", log)
	}
	if strings.Contains(log, "temp") || strings.Contains(log, "line1") {
		t.Errorf("build log leaks values:\n%s", log)
	}
}

func TestStaticPlanExportsBuildEnv(t *testing.T) {
	requireTools(t, "sh")
	tricky := `https://api/$HOME "q" \n`
	b := &kube.KusoBuild{Spec: kube.KusoBuildSpec{
		Strategy: "static",
		Static: &kube.KusoStaticSpec{
			BuildCmd:  `mkdir -p dist && printf '%s|%s|%s' "$VITE_API_URL" "$APP_VERSION" "${NODE_ENV-unset}" > dist/env.txt`,
			OutputDir: "dist",
		},
		BuildEnv:  map[string]string{"VITE_API_URL": tricky, "NODE_ENV": "production", "DATABASE_URL": "kuso-secret-ref://db-conn/URL"},
		BuildArgs: map[string]string{"APP_VERSION": "1.2.3"},
	}}
	c := renderStaticPlanContainer(b)
	for _, e := range c.Env {
		if e.ValueFrom != nil || strings.Contains(e.Value, "kuso-secret-ref") {
			t.Errorf("secret-sourced build env reached the static plan: %+v", e)
		}
	}
	dir := t.TempDir()
	cmd := exec.Command("sh", "-c", c.Args[0])
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	for _, e := range c.Env {
		cmd.Env = append(cmd.Env, e.Name+"="+e.Value)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	got, err := os.ReadFile(filepath.Join(dir, "dist", "env.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if want := tricky + "|1.2.3|unset"; string(got) != want {
		t.Errorf("build saw %q, want %q", got, want)
	}
}
