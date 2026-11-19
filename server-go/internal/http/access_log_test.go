package http

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// HTTP-2: on the deploy-hook, invite and review routes the {token} path
// segment is the credential, so the access log must not record it.
func TestSlogRequest_MasksTokenPathParam(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	r := chi.NewRouter()
	r.Use(slogRequest(slog.New(slog.NewJSONHandler(&buf, nil))))
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }
	r.Post("/api/hooks/deploy/{project}/{service}/{token}", ok)
	r.Group(func(g chi.Router) { g.Get("/api/invites/lookup/{token}", ok) })
	r.Route("/api/reviews", func(sr chi.Router) { sr.Post("/{token}/decision", ok) })

	for _, tc := range []struct{ method, path, want string }{
		{http.MethodPost, "/api/hooks/deploy/shop/web/SECRETHOOK0123", `"path":"/api/hooks/deploy/shop/web/{token}"`},
		{http.MethodGet, "/api/invites/lookup/SECRETINVITE0123", `"path":"/api/invites/lookup/{token}"`},
		{http.MethodPost, "/api/reviews/SECRETREVIEW0123/decision", `"path":"/api/reviews/{token}/decision"`},
	} {
		buf.Reset()
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(tc.method, tc.path, nil))
		line := buf.String()
		if strings.Contains(line, "SECRET") {
			t.Errorf("%s: access log leaked the token: %s", tc.path, line)
		}
		if !strings.Contains(line, tc.want) {
			t.Errorf("%s: want %s in %s", tc.path, tc.want, line)
		}
	}
}
