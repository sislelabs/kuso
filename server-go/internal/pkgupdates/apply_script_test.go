package pkgupdates

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"kuso/server/internal/testsh"
)

// curlStub plays the apiserver for drain_node: GET pods lists one pod on
// the node until it has been evicted, POST eviction returns 201.
const curlStub = `#!/bin/sh
case "$*" in
  *"-X POST"*) touch "$STATE/evicted"; printf 201 ;;
  *) if [ -f "$STATE/evicted" ]; then printf '{"items":[]}'; else
       printf '{"items":[{"metadata":{"namespace":"shop","name":"web-1","ownerReferences":[{"kind":"ReplicaSet"}]},"status":{"phase":"Running"}}]}'; fi ;;
esac
`

// runDrain runs the script's helpers plus drain_node with a PATH holding
// only the stubbed curl and the tools the script needs (jq optional).
func runDrain(t *testing.T, withJQ bool) (string, error) {
	t.Helper()
	prefix, _, ok := strings.Cut(applyScript, `echo "pkg-apply: node=$NODE`)
	if !ok {
		t.Fatal("script layout changed: no main section marker")
	}
	return runScript(t, prefix+"\ndrain_node\n", withJQ, map[string]string{"curl": curlStub}, nil)
}

// runScript runs script with a PATH of stubs (name → body) plus the real
// tools the script needs that aren't stubbed.
func runScript(t *testing.T, script string, withJQ bool, stubs map[string]string, env []string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	state := filepath.Join(dir, "state")
	for _, d := range []string{bin, state} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range stubs {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	tools := []string{"sh", "sort", "wc", "tr", "grep", "date", "sleep", "cat", "sed", "touch", "expr"}
	if withJQ {
		tools = append(tools, "jq")
	}
	for _, tool := range tools {
		if _, stubbed := stubs[tool]; stubbed {
			continue
		}
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s not installed", tool)
		}
		if err := os.Symlink(p, filepath.Join(bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	cmd := testsh.Command(t, script)
	cmd.Env = append([]string{
		"PATH=" + bin, "STATE=" + state, "NODE_NAME=n1",
		"POD_NAMESPACE=kuso", "POD_NAME=kuso-pkg-apply-n1-x",
		"KUBERNETES_SERVICE_HOST=127.0.0.1", "KUBERNETES_SERVICE_PORT=1",
		"APPLY_ANNOTATION=a", "CORDON_ANNOTATION=c",
	}, env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// LOOP-10: without jq the drain used to be skipped and the multi-node
// worker rebooted undrained. It must now report failure.
func TestDrainNodeFailsWithoutJQ(t *testing.T) {
	out, err := runDrain(t, false)
	if err == nil {
		t.Fatalf("drain_node succeeded without jq; output:\n%s", out)
	}
}

// LOOP-10: the drain waits until the evicted pod has left the node.
func TestDrainNodeEvictsAndWaitsForPods(t *testing.T) {
	out, err := runDrain(t, true)
	if err != nil {
		t.Fatalf("drain_node: %v\n%s", err, out)
	}
	for _, want := range []string{"evicted shop/web-1", "evicted pods have left n1"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// pdbCurlStub: two Ready nodes, one pod on n1 whose eviction a PDB
// refuses (429) forever.
const pdbCurlStub = `#!/bin/sh
case "$*" in
  *"-X POST"*"/eviction"*) printf 429 ;;
  *"-X PATCH"*) echo "$*" >> "$STATE/patches" ;;
  *"/api/v1/nodes"*) printf '{"items":[{"status":{"conditions":[{"type":"Ready","status":"True"}]}},{"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}' ;;
  *) printf '{"items":[{"metadata":{"namespace":"kuso","name":"kuso-postgres-0","ownerReferences":[{"kind":"StatefulSet"}]},"status":{"phase":"Running"}}]}' ;;
esac
`

// dateStub jumps 200s per "date +%s" so the 120s eviction deadline
// passes on the second try instead of after two real minutes.
const dateStub = `#!/bin/sh
if [ "$1" = "+%s" ]; then
  n=$(cat "$STATE/clock" 2>/dev/null || echo 1000)
  n=$(expr "$n" + 200)
  echo "$n" > "$STATE/clock"
  echo "$n"
else
  echo 2026-11-14T00:00:00Z
fi
`

// RUN-14: a PDB-blocked eviction (kuso-postgres allows 0 disruptions)
// was logged as "proceeding" and the node rebooted under the pod the PDB
// protects. The apply must fail and leave the node up instead.
func TestApplyDoesNotRebootPastPDBBlockedEviction(t *testing.T) {
	out, err := runScript(t, applyScript, true, map[string]string{
		"curl":    pdbCurlStub,
		"date":    dateStub,
		"sleep":   "#!/bin/sh\nexit 0\n",
		"nsenter": "#!/bin/sh\nexit 0\n",
		"apk":     "#!/bin/sh\nexit 0\n",
	}, []string{"ALLOW_REBOOT=true", "PKG_MGR=apt"})
	if err == nil {
		t.Fatalf("apply succeeded although a PDB blocked the drain:\n%s", out)
	}
	if strings.Contains(out, "rebooting n1") {
		t.Fatalf("rebooted past a PDB-blocked eviction:\n%s", out)
	}
	for _, want := range []string{"PodDisruptionBudget", "kuso/kuso-postgres-0", "not rebooting"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}
