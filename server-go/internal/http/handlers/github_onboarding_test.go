package handlers

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"kuso/server/internal/auth"
	"kuso/server/internal/db"
)

type memGithubCache struct {
	insts []db.GithubInstallation
	repos map[int64][]db.GithubRepo
}

func (m *memGithubCache) Upsert(context.Context, db.GithubInstallation) error    { return nil }
func (m *memGithubCache) SetRepos(context.Context, int64, []db.GithubRepo) error { return nil }
func (m *memGithubCache) List(context.Context) ([]db.GithubInstallation, error)  { return m.insts, nil }
func (m *memGithubCache) Repos(_ context.Context, id int64) ([]db.GithubRepo, error) {
	return m.repos[id], nil
}
func (m *memGithubCache) Delete(context.Context, int64) error { return nil }

func withPerms(req *http.Request, perms ...auth.Permission) *http.Request {
	ps := make([]string, 0, len(perms))
	for _, p := range perms {
		ps = append(ps, string(p))
	}
	return req.WithContext(auth.WithClaimsForTest(req.Context(), &auth.Claims{UserID: "u1", Permissions: ps}))
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestGithubRepos_EditorWithProjectsCreateGetsFlatList(t *testing.T) {
	t.Parallel()
	h := &GithubHandler{Logger: discardLogger(), Cache: &memGithubCache{
		insts: []db.GithubInstallation{
			{ID: 7, AccountLogin: "acme", RepositoriesJSON: `[{"fullName":"acme/web","defaultBranch":"main"}]`},
		},
		repos: map[int64][]db.GithubRepo{7: {{FullName: "acme/web", DefaultBranch: "main"}}},
	}}
	req := withPerms(httptest.NewRequest(http.MethodGet, "/api/github/repos", nil), auth.PermProjectsCreate)
	rr := httptest.NewRecorder()
	h.ListRepos(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var got []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	want := map[string]any{"fullName": "acme/web", "defaultBranch": "main", "installationId": float64(7)}
	if len(got[0]) != len(want) {
		t.Fatalf("unexpected fields in %+v", got[0])
	}
	for k, v := range want {
		if got[0][k] != v {
			t.Fatalf("%s=%v want %v", k, got[0][k], v)
		}
	}
}

func TestGithubRepos_RejectsViewer(t *testing.T) {
	t.Parallel()
	h := &GithubHandler{Cache: &memGithubCache{}}
	req := withPerms(httptest.NewRequest(http.MethodGet, "/api/github/repos", nil))
	rr := httptest.NewRecorder()
	h.ListRepos(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", rr.Code)
	}
}

func TestGithubRefreshInstallations_RejectsNonAdmin(t *testing.T) {
	t.Parallel()
	calls := 0
	h := &GithubHandler{refreshFn: func(context.Context) error { calls++; return nil }}
	req := withPerms(httptest.NewRequest(http.MethodPost, "/api/github/installations/refresh", nil), auth.PermProjectsCreate)
	rr := httptest.NewRecorder()
	h.RefreshInstallations(rr, req)
	if rr.Code != http.StatusForbidden || calls != 0 {
		t.Fatalf("status=%d calls=%d want 403/0", rr.Code, calls)
	}
}

func TestGithubRefreshInstallations_AdminRefreshes(t *testing.T) {
	t.Parallel()
	calls := 0
	h := &GithubHandler{Logger: discardLogger(), refreshFn: func(context.Context) error { calls++; return nil }}
	req := withPerms(httptest.NewRequest(http.MethodPost, "/api/github/installations/refresh", nil), auth.PermSettingsAdmin)
	rr := httptest.NewRecorder()
	h.RefreshInstallations(rr, req)
	if rr.Code != http.StatusOK || calls != 1 {
		t.Fatalf("status=%d calls=%d want 200/1", rr.Code, calls)
	}
}

func TestGithubSetupCallback_DebouncesRefreshAndRedirectsToLanding(t *testing.T) {
	t.Parallel()
	calls := 0
	h := &GithubHandler{
		Logger:    discardLogger(),
		refreshFn: func(context.Context) error { calls++; return nil },
	}
	for i := 0; i < 3; i++ {
		rr := httptest.NewRecorder()
		h.SetupCallback(rr, httptest.NewRequest(http.MethodGet, "/api/github/setup-callback?installation_id=1", nil))
		if rr.Code != http.StatusFound {
			t.Fatalf("status=%d", rr.Code)
		}
		if loc := rr.Header().Get("Location"); loc != "/github/installed" {
			t.Fatalf("Location=%q", loc)
		}
	}
	if calls != 1 {
		t.Fatalf("refresh ran %d times in the debounce window, want 1", calls)
	}
}
