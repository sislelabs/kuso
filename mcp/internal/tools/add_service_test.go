package tools

import (
	"net/http"
	"strings"
	"testing"
)

// The server auto-triggers a first build and reports its outcome; the
// tool used to discard it and say "added" even when the build never
// started.
func TestAddService_ReportsFirstBuildFailure(t *testing.T) {
	sess, reqs := newRecordingSession(t, func(r *http.Request) (int, string) {
		return http.StatusCreated, `{"metadata":{"name":"p-api"},"firstBuild":{"triggered":false,"error":"no GitHub installation"}}`
	})
	text, _, err := callText(t, sess, "add_service", map[string]any{
		"project": "p", "name": "api", "confirm": true, "skipFirstBuild": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "no GitHub installation") {
		t.Fatalf("first-build error lost: %s", text)
	}
	if got := reqs(); len(got) != 1 || !strings.Contains(got[0].Body, `"skipFirstBuild":true`) {
		t.Fatalf("skipFirstBuild not sent: %+v", got)
	}
}
