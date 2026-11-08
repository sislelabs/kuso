package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"kuso/server/internal/version"
)

// With update checks off there's no updater, but the running version is
// still known; the settings landing showed "Couldn't load" off a 404.
func TestGetVersion_WithoutUpdaterReportsRunningVersion(t *testing.T) {
	t.Parallel()
	h := &UpdaterHandler{}
	rec := httptest.NewRecorder()
	h.GetVersion(rec, httptest.NewRequest(http.MethodGet, "/api/system/version", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Current        string `json:"current"`
		ChecksDisabled bool   `json:"checksDisabled"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Current == "" || body.Current != version.Version() || !body.ChecksDisabled {
		t.Fatalf("body = %+v, want current=%q checksDisabled=true", body, version.Version())
	}
}
