package kube

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func helmTemplateRun(t *testing.T, sets ...string) string {
	t.Helper()
	helmBin, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm not found on PATH; skipping chart render test")
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	chart := filepath.Join(filepath.Dir(file), "..", "..", "..", "operator", "helm-charts", "kusorun")
	args := []string{
		"template", "alpha-web-run-abc", chart,
		"--set", "project=alpha",
		"--set", "service=web",
		"--set", "image.repository=registry.local/alpha/web",
		"--set", "image.tag=sha123",
		"--set", "command[0]=api",
		"--set", "command[1]=count",
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

// initBlock returns the rendered text between the wait-for-addons init
// container and the first main container, failing if either is missing or
// out of order.
func initBlock(t *testing.T, out, mainMarker string) string {
	t.Helper()
	initIdx := strings.Index(out, "- name: wait-for-addons")
	mainIdx := strings.Index(out, mainMarker)
	if initIdx < 0 {
		t.Fatalf("no wait-for-addons initContainer.\nRendered:\n%s", out)
	}
	if mainIdx < 0 || initIdx > mainIdx {
		t.Fatalf("wait-for-addons must render before %q.\nRendered:\n%s", mainMarker, out)
	}
	return out[initIdx:mainIdx]
}

func assertWaitInit(t *testing.T, init string) {
	t.Helper()
	for _, want := range []string{
		"image: busybox:1.36",
		"wait-for-addons: waiting for",
		`name: "alpha-db-conn"`,
		"allowPrivilegeEscalation: false",
		"drop:",
		"ALL",
		"WAIT_FOR_ADDONS_SOFT",
	} {
		if !strings.Contains(init, want) {
			t.Errorf("wait-for-addons init missing %q.\n%s", want, init)
		}
	}
}

// A fresh one-shot pod's IP isn't in kube-router's netpol allow ipsets for
// ~5-20s, so `kuso run … -- api count` got connection refused on the DB and
// exited 1. The run Job must TCP-wait on its addons first, with the same env
// the run container sees.
func TestKusoRunChart_WaitForAddonsInit(t *testing.T) {
	out := helmTemplateRun(t,
		"envFromSecrets[0]=alpha-db-conn",
		"env[0].name=FOO",
		"env[0].value=bar",
		"env[1].name=DATABASE_URI",
		"env[1].valueFrom.secretKeyRef.name=alpha-db-conn",
		"env[1].valueFrom.secretKeyRef.key=DATABASE_URL",
	)
	init := initBlock(t, out, "- name: run")
	assertWaitInit(t, init)
	for _, want := range []string{`name: "FOO"`, `name: "DATABASE_URI"`, "secretKeyRef:"} {
		if !strings.Contains(init, want) {
			t.Errorf("init must carry the run's inline env (%q).\n%s", want, init)
		}
	}
	if !strings.Contains(out, "automountServiceAccountToken: false") {
		t.Errorf("run pod must keep automountServiceAccountToken: false")
	}
}

func TestKusoCronChart_WaitForAddonsInit(t *testing.T) {
	out := helmTemplateCron(t,
		"service=web",
		"image.repository=registry.local/alpha/web",
		"image.tag=sha123",
		"command[0]=api",
		"envFromSecrets[0]=alpha-db-conn",
	)
	assertWaitInit(t, initBlock(t, out, "- name: cron"))
}

// kind=http runs curlimages/curl against a URL; it carries no addon work to
// wait for, so it gets no init.
func TestKusoCronChart_HTTPKindHasNoWaitInit(t *testing.T) {
	out := helmTemplateCron(t, "kind=http", "url=https://example.com/health", "envFromSecrets[0]=alpha-db-conn")
	if strings.Contains(out, "wait-for-addons") {
		t.Errorf("kind=http cron should not get a wait-for-addons init.\n%s", out)
	}
}
