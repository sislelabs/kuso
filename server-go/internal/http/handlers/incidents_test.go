package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"kuso/server/internal/auth"
	"kuso/server/internal/db"
)

// incidentTokenAuth + bearerToken are the agent-endpoint security
// boundary; resolveFeedbackAction is the decision router that decides
// whether the operator's reply triggers a write (implement) or not. Both
// are pure and tested without HTTP/DB plumbing.

func reqWithAuth(header string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/incidents/inc-1/findings", nil)
	if header != "" {
		r.Header.Set("Authorization", header)
	}
	return r
}

func TestBearerToken(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   string
	}{
		{"valid", "Bearer abc123", "abc123"},
		{"case-insensitive scheme", "bearer abc123", "abc123"},
		{"trims surrounding space", "Bearer   abc123  ", "abc123"},
		{"missing header", "", ""},
		{"wrong scheme", "Basic abc123", ""},
		{"scheme only", "Bearer ", ""},
		{"no scheme", "abc123", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bearerToken(reqWithAuth(tt.header)); got != tt.want {
				t.Fatalf("bearerToken(%q) = %q, want %q", tt.header, got, tt.want)
			}
		})
	}
}

func TestIncidentTokenAuth(t *testing.T) {
	const tok = "iat-deadbeefcafef00d"
	in := db.Incident{ID: "inc-1", AgentToken: tok}

	tests := []struct {
		name      string
		header    string
		incident  db.Incident
		wantAllow bool
	}{
		{"correct token", "Bearer " + tok, in, true},
		{"wrong token", "Bearer iat-wrongtoken", in, false},
		{"empty header", "", in, false},
		{"missing scheme", tok, in, false},
		{"empty stored token never matches empty presented", "Bearer ", db.Incident{ID: "inc-2"}, false},
		{"empty stored token, real presented", "Bearer " + tok, db.Incident{ID: "inc-3"}, false},
		{"prefix of token rejected", "Bearer " + tok[:8], in, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := incidentTokenAuth(reqWithAuth(tt.header), tt.incident); got != tt.wantAllow {
				t.Fatalf("incidentTokenAuth = %v, want %v", got, tt.wantAllow)
			}
		})
	}
}

func TestResolveFeedbackAction(t *testing.T) {
	tests := []struct {
		decision string
		want     feedbackAction
	}{
		{"go", feedbackGo},
		{"GO", feedbackGo},
		{" go ", feedbackGo},
		{"approve", feedbackGo},
		{"approved", feedbackGo},
		{"reject", feedbackReject},
		{"REJECT", feedbackReject},
		{"rejected", feedbackReject},
		{"no", feedbackReject},
		{"", feedbackComment},
		{"maybe", feedbackComment},
		{"goose", feedbackComment}, // not a prefix match — must be a comment, not a write
	}
	for _, tt := range tests {
		t.Run(tt.decision, func(t *testing.T) {
			if got := resolveFeedbackAction(tt.decision); got != tt.want {
				t.Fatalf("resolveFeedbackAction(%q) = %d, want %d", tt.decision, got, tt.want)
			}
		})
	}
}

func TestIncidentsList_PagesWithTruncationHeaders(t *testing.T) {
	d := openTestDB(t)
	if _, err := d.DB.Exec(`TRUNCATE TABLE "Incident"`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("inc-h%d", i)
		if err := d.CreateIncident(ctx, db.Incident{ID: id, EventType: "pod.crashed", TargetKey: "k|" + id,
			State: db.IncidentResolved, Title: id, Severity: "warn"}); err != nil {
			t.Fatal(err)
		}
	}
	h := &IncidentsHandler{DB: d}
	admin := &auth.Claims{UserID: "u1", Permissions: []string{string(auth.PermSettingsAdmin)}}
	get := func(q string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/incidents"+q, nil)
		r = r.WithContext(auth.WithClaimsForTest(r.Context(), admin))
		w := httptest.NewRecorder()
		h.List(w, r)
		return w
	}
	w := get("?limit=2")
	if w.Code != http.StatusOK || w.Header().Get(headerTruncated) != "true" || w.Header().Get(headerNextOffset) != "2" {
		t.Fatalf("first page: code=%d truncated=%q next=%q", w.Code, w.Header().Get(headerTruncated), w.Header().Get(headerNextOffset))
	}
	w = get("?limit=2&offset=2")
	var body struct{ Incidents []db.Incident }
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Incidents) != 1 || w.Header().Get(headerTruncated) != "" {
		t.Errorf("last page: %d rows, truncated=%q; want 1 row, no header", len(body.Incidents), w.Header().Get(headerTruncated))
	}
}
