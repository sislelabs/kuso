package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A chunked body (ContentLength -1) used to be skipped entirely, so a
// malformed or staging-targeted request restarted production.
func TestRestart_ChunkedBadBodyIs400(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/projects/p/services/s/restart", strings.NewReader(`{"env":`))
	req.ContentLength = -1
	rr := httptest.NewRecorder()
	(&BuildsHandler{}).Restart(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status %d (want 400)", rr.Code)
	}
}
