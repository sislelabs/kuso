package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/auth"
	"kuso/server/internal/builds"
)

type branchRemote struct {
	envExampleRemote
	calls int
	err   error
}

func (r *branchRemote) BranchNames(context.Context, string, string) (string, []string, error) {
	r.calls++
	if r.err != nil {
		return "", nil, r.err
	}
	return "main", []string{"feature-x", "main"}, nil
}

func getBranches(t *testing.T, remote *branchRemote, ctx context.Context) *httptest.ResponseRecorder {
	t.Helper()
	h, _ := envExampleHandler(t)
	h.Svc.Remote = remote
	r := chi.NewRouter()
	h.Mount(r)
	req := httptest.NewRequest(http.MethodGet, "/api/projects/alpha/services/api/branches", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestBranches_ListsRemoteBranches(t *testing.T) {
	t.Parallel()
	remote := &branchRemote{}
	admin := auth.WithClaimsForTest(context.Background(),
		&auth.Claims{UserID: "u1", Permissions: []string{string(auth.PermSettingsAdmin)}})
	rec := getBranches(t, remote, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body)
	}
	var out builds.RepoBranches
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.DefaultBranch != "main" || len(out.Branches) != 2 || out.Branches[0] != "feature-x" {
		t.Errorf("got %+v", out)
	}
}

func TestBranches_RefusedCallersNeverReachTheRemote(t *testing.T) {
	t.Parallel()
	remote := &branchRemote{}
	outsider := auth.WithClaimsForTest(context.Background(), &auth.Claims{UserID: "u2", Permissions: []string{}})
	if rec := getBranches(t, remote, outsider); rec.Code != http.StatusForbidden {
		t.Fatalf("outsider got %d, want 403", rec.Code)
	}
	if remote.calls != 0 {
		t.Errorf("remote listed %d times for a refused caller", remote.calls)
	}
}

func TestBranches_UnreachableRemoteIsBadGateway(t *testing.T) {
	t.Parallel()
	remote := &branchRemote{err: errors.New("HTTP 404")}
	admin := auth.WithClaimsForTest(context.Background(),
		&auth.Claims{UserID: "u1", Permissions: []string{string(auth.PermSettingsAdmin)}})
	if rec := getBranches(t, remote, admin); rec.Code != http.StatusBadGateway {
		t.Fatalf("got %d, want 502: %s", rec.Code, rec.Body)
	}
}
