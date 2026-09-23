package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/auth"
	"kuso/server/internal/db"
)

// The scanner stores ErrorEvent.service as the log line's service, which is
// the FQ `<project>-<service>` form. The CLI and web call the endpoint with
// the short name, so the handler must widen it or every lookup returns [].
func TestErrorsList_ShortServiceNameFindsFQNEvents(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	if err := d.InsertErrorEvent(ctx, db.ErrorEvent{
		Project: "tickero", Service: "tickero-api", Env: "production", Pod: "p",
		Fingerprint: "fp1", Message: "ERROR boom", RawLine: "ERROR boom",
		Ts: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	for _, svc := range []string{"api", "tickero-api"} {
		h := &ErrorsHandler{DB: d, Logger: slog.Default()}
		r := chi.NewRouter()
		h.Mount(r)
		req := httptest.NewRequest(http.MethodGet, "/api/projects/tickero/services/"+svc+"/errors", nil)
		req = req.WithContext(auth.WithClaimsForTest(req.Context(),
			&auth.Claims{UserID: "u1", Permissions: []string{string(auth.PermSettingsAdmin)}}))
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: status=%d body=%s", svc, rr.Code, rr.Body)
		}
		var groups []db.ErrorGroup
		if err := json.Unmarshal(rr.Body.Bytes(), &groups); err != nil {
			t.Fatalf("%s: decode: %v", svc, err)
		}
		if len(groups) != 1 || groups[0].Count != 1 {
			t.Errorf("service=%q: got %d groups %+v, want the one tickero-api event", svc, len(groups), groups)
		}
	}
}
