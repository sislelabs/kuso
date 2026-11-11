package buildcontroller

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"kuso/server/internal/builds"
)

const fakeDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// buildctlStub stands in for buildctl: `debug workers` succeeds only in the
// daemon's mode (TLS flags present iff tlsDaemon), `build` logs its argv
// and the manifest-push line the script parses for the digest.
func buildctlStub(t *testing.T, dir string, tlsDaemon bool) {
	t.Helper()
	want := "plain"
	if tlsDaemon {
		want = "tls"
	}
	stub := `#!/bin/bash
mode=plain
for a in "$@"; do [ "$a" = "--tlscert" ] && mode=tls; done
for a in "$@"; do
  if [ "$a" = "workers" ]; then [ "$mode" = "` + want + `" ]; exit $?; fi
done
printf '%s\n' "$@" > "` + dir + `/argv"
echo "#9 pushing manifest for ${IMAGE_REF}@` + fakeDigest + ` 0.1s done"
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "buildctl"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
}

func runBuildkitScript(t *testing.T, c corev1.Container, dir string, tlsMounted bool) []string {
	t.Helper()
	env := []string{"PATH=" + dir + ":" + os.Getenv("PATH"), "KUSO_TERMINATION_LOG=" + filepath.Join(dir, "term")}
	for _, e := range c.Env {
		if e.ValueFrom == nil {
			env = append(env, e.Name+"="+e.Value)
		}
	}
	script := c.Args[0]
	tlsDir := filepath.Join(dir, "tls")
	if tlsMounted {
		if err := os.MkdirAll(tlsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, f := range []string{"ca.crt", "tls.crt", "tls.key"} {
			_ = os.WriteFile(filepath.Join(tlsDir, f), []byte("x"), 0o600)
		}
	}
	script = strings.ReplaceAll(script, "TLSD=/tmp/buildkit-tls", "TLSD="+tlsDir)
	script = strings.ReplaceAll(script, "/tmp/buildctl.", dir+"/buildctl.")
	cmd := exec.Command("bash", "-ec", script)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "argv"))
	if err != nil {
		t.Fatalf("buildctl build never ran:\n%s", out)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

// OPS-3: against a daemon that requires mTLS the build call carries the
// client certificate; against a plaintext daemon it connects without it.
// ISO-7/OPS-4: cache mounts are namespaced per service (and preview), and
// a user build-arg of the same name can't override that. The pushed
// digest is written to the termination message for digest pinning.
func TestBuildkitScript_TLSCacheNamespaceAndDigest(t *testing.T) {
	requireTools(t, "bash", "sed", "tee", "seq")
	for _, tc := range []struct {
		name               string
		tlsDaemon, mounted bool
		preview            string
		wantTLS            bool
		wantNS             string
	}{
		{name: "tls daemon", tlsDaemon: true, mounted: true, wantTLS: true, wantNS: "alpha/api"},
		{name: "plain daemon with certs", mounted: true, wantNS: "alpha/api"},
		{name: "plain daemon without certs", preview: "alpha-api-pr-7", wantNS: "alpha/api/alpha-api-pr-7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			buildctlStub(t, dir, tc.tlsDaemon)
			b := baseBuild()
			b.Spec.BuildArgs = map[string]string{"BUILDKIT_CACHE_MOUNT_NS": "beta/web"}
			if tc.preview != "" {
				b.Annotations = map[string]string{builds.AnnPreviewEnv: tc.preview}
			}
			argv := runBuildkitScript(t, renderBuildkitContainer(b, "dockerfile", corev1.ResourceRequirements{}), dir, tc.mounted)
			joined := strings.Join(argv, " ")
			if got := strings.Contains(joined, "--tlscert"); got != tc.wantTLS {
				t.Fatalf("TLS flags on build = %v, want %v: %s", got, tc.wantTLS, joined)
			}
			last := ""
			for _, a := range argv {
				if strings.HasPrefix(a, "build-arg:BUILDKIT_CACHE_MOUNT_NS=") {
					last = strings.TrimPrefix(a, "build-arg:BUILDKIT_CACHE_MOUNT_NS=")
				}
			}
			if last != tc.wantNS {
				t.Fatalf("effective BUILDKIT_CACHE_MOUNT_NS = %q, want %q", last, tc.wantNS)
			}
			term, _ := os.ReadFile(filepath.Join(dir, "term"))
			if strings.TrimSpace(string(term)) != "digest="+fakeDigest {
				t.Fatalf("termination message = %q", term)
			}
		})
	}
}

// The client certificate is mounted into the buildctl container only:
// static-plan runs the user's buildCmd and must never see it.
func TestBuildkitClientCertOnlyInBuildkitContainer(t *testing.T) {
	b := baseBuild()
	b.Spec.Strategy = "static"
	spec := renderPodSpec("b1", b, "static", corev1.ResourceRequirements{})
	for _, c := range append(spec.InitContainers, spec.Containers...) {
		has := false
		for _, m := range c.VolumeMounts {
			if m.Name == "buildkit-tls" {
				has = true
			}
		}
		if has != (c.Name == "buildkit") {
			t.Fatalf("container %s: buildkit-tls mounted = %v", c.Name, has)
		}
	}
}
