package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

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
