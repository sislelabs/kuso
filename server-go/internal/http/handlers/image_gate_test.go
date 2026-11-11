package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kuso/server/internal/imagecheck"
	"kuso/server/internal/projects"
)

// RUN-5: setting a tag the registry says doesn't exist is refused before
// any env is touched; an existing tag (or a registry that can't answer)
// goes through.
func TestRefuseMissingImage(t *testing.T) {
	t.Parallel()
	reg := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/manifests/good"):
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/manifests/flaky"):
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer reg.Close()
	host := strings.TrimPrefix(reg.URL, "https://")
	h := &ProjectsHandler{Svc: &imageCheckFakeSvc{}, Logger: slog.Default(), Namespace: "kuso",
		ImageChecker: &imagecheck.Checker{HTTP: reg.Client()}}

	cases := []struct {
		tag, query string
		refused    bool
	}{
		{"v9.9.9-nope", "", true},
		{"good", "", false},
		{"flaky", "", false},
		{"v9.9.9-nope", "skipImageCheck=true", false},
	}
	for _, c := range cases {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPatch, "/x?"+c.query, nil)
		got := h.refuseMissingImage(context.Background(), rr, req, "p1", "web",
			&projects.ServiceImageSpec{Repository: host + "/acme/web", Tag: c.tag})
		if got != c.refused {
			t.Errorf("tag %q query %q: refused = %v, want %v", c.tag, c.query, got, c.refused)
		}
		if c.refused && rr.Code != http.StatusUnprocessableEntity {
			t.Errorf("tag %q: status %d, want 422", c.tag, rr.Code)
		}
	}
}
