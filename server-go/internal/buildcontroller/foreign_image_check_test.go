package buildcontroller

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type foreignCase struct {
	name       string
	dockerfile string
	buildArg   string // value of build arg BASE, "" = none
	policy     string
	wantFail   bool
	wantHits   []string
	wantClean  bool
}

var foreignCases = []foreignCase{
	{name: "own project", dockerfile: "FROM kuso-registry.kuso.svc.cluster.local:5000/shop/base:1\nCOPY --from=kuso-registry:5000/shop/api:abc /app /app\n",
		policy: "refuse", wantClean: true},
	{name: "external registry with a similar name", dockerfile: "FROM my-kuso-registry.example.com/blog/api:1\nFROM ghcr.io/kuso-registry/x:1\n",
		policy: "refuse", wantClean: true},
	{name: "foreign FROM refused on previews", dockerfile: "FROM kuso-registry.kuso.svc.cluster.local:5000/blog/api:abc AS src\nFROM alpine\n",
		policy: "refuse", wantFail: true, wantHits: []string{"kuso-registry.kuso.svc.cluster.local:5000/blog/api:abc"}},
	{name: "foreign COPY --from and ARG default", dockerfile: "ARG BASE=kuso-registry:5000/blog/base:1\nFROM alpine\nCOPY --from=kuso-registry.kuso:5000/blog/api:abc / /x\n",
		policy: "refuse", wantFail: true, wantHits: []string{"BASE=kuso-registry:5000/blog/base:1", "--from=kuso-registry.kuso:5000/blog/api:abc"}},
	{name: "foreign ref warns on non-preview builds", dockerfile: "FROM kuso-registry:5000/blog/api:abc\n",
		policy: "warn", wantHits: []string{"kuso-registry:5000/blog/api:abc"}},
	{name: "foreign build arg reported by key only", dockerfile: "ARG BASE\nFROM ${BASE}\n", buildArg: "kuso-registry:5000/blog/api:secretish",
		policy: "refuse", wantFail: true, wantHits: []string{"build arg BASE"}},
}

func foreignScript() string {
	return "set -e\n" + foreignImageCheckScript + "\necho KUSO_CHECK_DONE\n"
}

func checkForeignResult(t *testing.T, tc foreignCase, out string, err error) {
	t.Helper()
	if tc.wantFail != (err != nil) {
		t.Fatalf("exit err=%v, wantFail=%v\n%s", err, tc.wantFail, out)
	}
	if tc.wantClean && strings.Contains(out, "WARNING") {
		t.Errorf("flagged a clean build:\n%s", out)
	}
	for _, h := range tc.wantHits {
		if !strings.Contains(out, "  "+h+"\n") {
			t.Errorf("missing hit %q in:\n%s", h, out)
		}
	}
	if strings.Contains(out, "secretish") {
		t.Errorf("a build-arg value was printed:\n%s", out)
	}
}

func writeDockerfile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func foreignEnv(ctx string, tc foreignCase) []string {
	env := []string{"CTX=" + ctx, "DF=Dockerfile", "KUSO_BUILD_PROJECT=shop", "FOREIGN_IMAGE_POLICY=" + tc.policy, "PATH=" + os.Getenv("PATH")}
	if tc.buildArg != "" {
		env = append(env, "KUSO_BUILDARG_KEYS=BASE", "KUSO_BA_BASE="+tc.buildArg)
	}
	return env
}

// BLOB-R4 stopgap: a build may only name its own project's images on the
// in-cluster registry.
func TestForeignImageCheck(t *testing.T) {
	for _, tc := range foreignCases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("sh", "-c", foreignScript())
			cmd.Env = foreignEnv(writeDockerfile(t, tc.dockerfile), tc)
			out, err := cmd.CombinedOutput()
			checkForeignResult(t, tc, string(out), err)
		})
	}
}

// Same cases in the image the build container really runs (busybox sh).
func TestForeignImageCheck_Docker(t *testing.T) {
	if os.Getenv("KUSO_TEST_DOCKER") == "" {
		t.Skip("docker-backed test: set KUSO_TEST_DOCKER=1 (make test-docker) to run it")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal("KUSO_TEST_DOCKER is set but docker is not on PATH")
	}
	for _, tc := range foreignCases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeDockerfile(t, tc.dockerfile)
			args := []string{"run", "--rm", "-v", dir + ":/w:ro", "--entrypoint", "sh"}
			for _, e := range foreignEnv("/w", tc) {
				if !strings.HasPrefix(e, "PATH=") {
					args = append(args, "-e", e)
				}
			}
			args = append(args, defaultBuildkitImage, "-c", foreignScript())
			out, err := exec.Command(docker, args...).CombinedOutput()
			if err != nil && !strings.Contains(string(out), "ERROR: preview builds") {
				t.Fatalf("docker run: %v\n%s", err, out)
			}
			checkForeignResult(t, tc, string(out), err)
		})
	}
}
