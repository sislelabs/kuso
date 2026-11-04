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
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	state := filepath.Join(dir, "state")
	for _, d := range []string{bin, state} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bin, "curl"), []byte(curlStub), 0o755); err != nil {
		t.Fatal(err)
	}
	tools := []string{"sh", "sort", "wc", "tr", "grep", "date", "sleep", "cat", "sed", "touch"}
	if withJQ {
		tools = append(tools, "jq")
	}
	for _, tool := range tools {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s not installed", tool)
		}
		if err := os.Symlink(p, filepath.Join(bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	prefix, _, ok := strings.Cut(applyScript, `echo "pkg-apply: node=$NODE`)
	if !ok {
		t.Fatal("script layout changed: no main section marker")
	}
	cmd := testsh.Command(t, prefix+"\ndrain_node\n")
	cmd.Env = []string{
		"PATH=" + bin, "STATE=" + state, "NODE_NAME=n1",
		"POD_NAMESPACE=kuso", "POD_NAME=kuso-pkg-apply-n1-x",
		"KUBERNETES_SERVICE_HOST=127.0.0.1", "KUBERNETES_SERVICE_PORT=1",
		"APPLY_ANNOTATION=a", "CORDON_ANNOTATION=c",
	}
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
