package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/auth"
)

func getEventTypes(t *testing.T, perms []string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	(&NotificationsHandler{}).Mount(r)
	ctx := auth.WithClaimsForTest(context.Background(), &auth.Claims{UserID: "u1", Permissions: perms})
	req := httptest.NewRequest(http.MethodGet, "/api/notifications/event-types", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestEventTypes_ServesCatalogue(t *testing.T) {
	rec := getEventTypes(t, []string{string(auth.PermSettingsAdmin)})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	// Decode into raw maps so the test pins the exact wire keys the web
	// UI is built against, not just the Go struct.
	var got []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	byType := map[string]map[string]any{}
	for _, e := range got {
		if len(e) != 4 {
			t.Errorf("entry has keys %v, want exactly type/label/group/defaultMention", e)
		}
		byType[e["type"].(string)] = e
	}
	if e := byType["build.succeeded"]; e == nil || e["label"] != "Build succeeded" || e["group"] != "build" || e["defaultMention"] != "" {
		t.Errorf("build.succeeded entry = %v", e)
	}
	if e := byType["addon.crashed"]; e == nil || e["group"] != "runtime" || e["defaultMention"] != "@here" {
		t.Errorf("addon.crashed entry = %v", e)
	}
	if byType["node.updates-applied"] == nil {
		t.Error("node.updates-applied missing")
	}
	for _, dead := range []string{"build.started", "deploy.rolled", "test.ping"} {
		if byType[dead] != nil {
			t.Errorf("%s must not be offered", dead)
		}
	}
	groups := map[string]bool{"build": true, "runtime": true, "jobs": true, "nodes": true, "backups": true, "other": true}
	for typ, e := range byType {
		if !groups[e["group"].(string)] {
			t.Errorf("%s has unknown group %v", typ, e["group"])
		}
	}
}

func TestEventTypes_AdminOnlyLikeList(t *testing.T) {
	if rec := getEventTypes(t, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin status = %d, want 403", rec.Code)
	}
}
