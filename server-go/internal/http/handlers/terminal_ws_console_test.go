package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// HTTP-7: kuso.console.enabled=false must refuse the browser terminal
// before any auth or pod lookup, instead of only being echoed in the session.
func TestTerminalRefusedWhenConsoleDisabled(t *testing.T) {
	t.Parallel()
	h := &TerminalWSHandler{ConsoleEnabled: func() bool { return false }}
	rec := httptest.NewRecorder()
	h.Terminal(rec, httptest.NewRequest(http.MethodGet, "/ws/projects/p/services/s/terminal", nil))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "console.enabled") {
		t.Fatalf("got %d %s, want 403 naming console.enabled", rec.Code, rec.Body.String())
	}
}
