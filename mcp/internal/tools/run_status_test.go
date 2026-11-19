package tools

import (
	"net/http"
	"strings"
	"testing"
)

// run returned phase=pending and nothing could read the run afterwards,
// so an agent couldn't learn whether its migration succeeded.
func TestRunStatus_ReportsPhaseAndFencedMessage(t *testing.T) {
	sess, reqs := newRecordingSession(t, func(r *http.Request) (int, string) {
		return http.StatusOK, `{"metadata":{"name":"p-api-run-abc","annotations":{"kuso.sislelabs.com/run-phase":"failed","kuso.sislelabs.com/run-message":"exit code 1"}},"spec":{"project":"p","service":"api"}}`
	})
	text, isErr, err := callText(t, sess, "run_status", map[string]any{"project": "p", "run": "p-api-run-abc"})
	if err != nil || isErr {
		t.Fatalf("run_status failed: err=%v text=%s", err, text)
	}
	if !strings.Contains(text, "phase=failed") || !strings.Contains(text, "exit code 1") || !strings.Contains(text, untrustedWarn) {
		t.Fatalf("text %s", text)
	}
	if got := reqs(); len(got) != 1 || got[0].Method != http.MethodGet || got[0].Path != "/api/projects/p/runs/p-api-run-abc" {
		t.Fatalf("requests %+v", got)
	}
}
