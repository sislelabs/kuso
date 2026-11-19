package tools

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// OPS-1: the server sets existing=true when a trigger coalesced into an
// in-flight build; the tool used to report that as "triggered".
func TestBuildTriggerTextHonoursExisting(t *testing.T) {
	var out buildSummary
	if err := json.Unmarshal([]byte(`{"id":"b1","status":"running","branch":"main","existing":true}`), &out); err != nil {
		t.Fatal(err)
	}
	text := buildTriggerText(out)
	if strings.Contains(text, "triggered") || !strings.Contains(text, "coalesced into b1") {
		t.Errorf("coalesced trigger text = %q", text)
	}
	out.Existing = false
	if text := buildTriggerText(out); !strings.HasPrefix(text, "build b1 triggered") {
		t.Errorf("fresh trigger text = %q", text)
	}
}

// build_status advertised env/branch but took list[0] across every env,
// so a failed staging build read as the newer preview's success.
func TestBuildStatus_FiltersByEnvAndBranch(t *testing.T) {
	sess, reqs := newRecordingSession(t, func(r *http.Request) (int, string) {
		if strings.HasSuffix(r.URL.Path, "/builds/latest") {
			return http.StatusOK, `{"api":{"id":"b-staging","branch":"staging","status":"failed"}}`
		}
		return http.StatusOK, `[{"id":"b-preview","branch":"pr-7","status":"succeeded"},{"id":"b-staging","branch":"staging","status":"failed"}]`
	})
	text, _, err := callText(t, sess, "build_status", map[string]any{"project": "p", "service": "api", "env": "staging"})
	if err != nil || !strings.Contains(text, "b-staging: status=failed") {
		t.Fatalf("env filter: err=%v text=%s", err, text)
	}
	if got := reqs(); got[len(got)-1].Path != "/api/projects/p/builds/latest" {
		t.Fatalf("env must use the env-aware latest view, got %+v", got)
	}
	text, _, err = callText(t, sess, "build_status", map[string]any{"project": "p", "service": "api", "branch": "staging"})
	if err != nil || !strings.Contains(text, "b-staging: status=failed") {
		t.Fatalf("branch filter: err=%v text=%s", err, text)
	}
}
