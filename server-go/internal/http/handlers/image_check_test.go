package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/auth"
	"kuso/server/internal/imagecheck"
	"kuso/server/internal/kube"
)

type imageCheckFakeSvc struct {
	ProjectsAPI
	pullSecret string
}

func (f *imageCheckFakeSvc) GetService(context.Context, string, string) (*kube.KusoService, error) {
	svc := &kube.KusoService{}
	svc.Spec.Image = &kube.KusoImage{Repository: "ghcr.io/acme/web", Tag: "1", PullSecret: f.pullSecret}
	return svc, nil
}

func imageCheckRequest(t *testing.T, h *ProjectsHandler, claims *auth.Claims, query string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Get("/api/projects/{project}/services/{service}/image-check", h.ImageCheck)
	req := httptest.NewRequest(http.MethodGet, "/api/projects/p1/services/web/image-check?"+query, nil)
	req = req.WithContext(auth.WithClaimsForTest(req.Context(), claims))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

func TestImageCheck(t *testing.T) {
	t.Parallel()
	admin := &auth.Claims{UserID: "admin", Permissions: []string{string(auth.PermSettingsAdmin)}}

	h := &ProjectsHandler{Svc: &imageCheckFakeSvc{}, Logger: slog.Default(), Namespace: "kuso"}
	if rr := imageCheckRequest(t, h, &auth.Claims{UserID: "outsider"}, "repository=nginx&tag=alpine"); rr.Code != http.StatusForbidden {
		t.Fatalf("non-member status = %d, want 403", rr.Code)
	}
	if rr := imageCheckRequest(t, h, admin, "repository=Not+Valid&tag=x"); rr.Code != http.StatusBadRequest {
		t.Fatalf("bad ref status = %d, want 400", rr.Code)
	}

	// A credentialed service is never judged anonymously (no request goes out).
	h = &ProjectsHandler{Svc: &imageCheckFakeSvc{pullSecret: "p1-regcred-ghcr-io"}, Logger: slog.Default(), Namespace: "kuso"}
	rr := imageCheckRequest(t, h, admin, "repository=ghcr.io/acme/web&tag=2")
	var got imagecheck.Result
	if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &got) != nil || got.Status != imagecheck.Unknown {
		t.Fatalf("private: status = %d body = %s, want 200 unknown", rr.Code, rr.Body.String())
	}
}
