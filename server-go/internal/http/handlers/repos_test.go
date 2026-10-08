package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/github"
	"kuso/server/internal/gitremote"
)

type fakeInspector struct {
	info      *gitremote.Info
	refsErr   error
	detected  *github.DetectedRuntime
	detectErr error
	gotToken  string
	gotBranch string
	gotPath   string
}

func (f *fakeInspector) Refs(_ context.Context, _, token string) (*gitremote.Info, error) {
	f.gotToken = token
	return f.info, f.refsErr
}

func (f *fakeInspector) DetectRuntime(_ context.Context, _, branch, path, _ string) (*github.DetectedRuntime, error) {
	f.gotBranch, f.gotPath = branch, path
	return f.detected, f.detectErr
}

func inspect(f *fakeInspector, body string) (*httptest.ResponseRecorder, inspectRepoResponse) {
	r := chi.NewRouter()
	(&ReposHandler{Remote: f, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}).Mount(r)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/repos/inspect", strings.NewReader(body)))
	var out inspectRepoResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func twoBranches() *gitremote.Info {
	return &gitremote.Info{DefaultBranch: "main", Branches: []gitremote.Branch{{Name: "develop", SHA: "a"}, {Name: "main", SHA: "b"}}}
}

func TestInspectRepo_PublicRepoReturnsBranchesAndRuntime(t *testing.T) {
	f := &fakeInspector{info: twoBranches(), detected: &github.DetectedRuntime{Runtime: "dockerfile", Port: 9090, Reason: "Dockerfile detected"}}
	rec, out := inspect(f, `{"url":"https://github.com/acme/app"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if out.DefaultBranch != "main" || len(out.Branches) != 2 || out.Branches[0] != "develop" {
		t.Errorf("branches = %+v default %q", out.Branches, out.DefaultBranch)
	}
	if out.Runtime == nil || out.Runtime.Runtime != "dockerfile" || out.Runtime.Port != 9090 {
		t.Errorf("runtime = %+v", out.Runtime)
	}
	if f.gotBranch != "main" {
		t.Errorf("detection ran on %q, want the default branch", f.gotBranch)
	}
}

func TestInspectRepo_ExplicitBranchAndPathDriveDetection(t *testing.T) {
	f := &fakeInspector{info: twoBranches(), detected: &github.DetectedRuntime{Runtime: "nixpacks", Port: 3000}}
	inspect(f, `{"url":"https://github.com/acme/app","branch":"develop","path":"apps/web","token":"pat"}`)
	if f.gotBranch != "develop" || f.gotPath != "apps/web" || f.gotToken != "pat" {
		t.Errorf("got branch=%q path=%q token=%q", f.gotBranch, f.gotPath, f.gotToken)
	}
}

func TestInspectRepo_PrivateOrMissingRepoAsksForAToken(t *testing.T) {
	f := &fakeInspector{refsErr: fmt.Errorf("%w: x", gitremote.ErrNotAccessible)}
	rec, _ := inspect(f, `{"url":"https://github.com/acme/private"}`)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "token") {
		t.Errorf("status = %d body %s, want 404 mentioning a token", rec.Code, rec.Body)
	}
}

// Detection is best-effort: an unsupported host or a rate limit still
// returns the branches so the user can pick the runtime by hand.
func TestInspectRepo_DetectionFailureStillReturnsBranches(t *testing.T) {
	f := &fakeInspector{info: twoBranches(), detectErr: fmt.Errorf("%w: only github", gitremote.ErrUnsupported)}
	rec, out := inspect(f, `{"url":"https://git.example.org/acme/app"}`)
	if rec.Code != http.StatusOK || len(out.Branches) != 2 {
		t.Fatalf("status = %d, branches %+v", rec.Code, out.Branches)
	}
	if out.Runtime != nil || out.RuntimeNote == "" {
		t.Errorf("runtime = %+v note = %q, want no runtime and a note", out.Runtime, out.RuntimeNote)
	}
}

func TestInspectRepo_RejectsURLsBuildsWouldReject(t *testing.T) {
	for _, u := range []string{"", "file:///etc/passwd", "ssh://git@github.com/a/b"} {
		rec, _ := inspect(&fakeInspector{info: twoBranches()}, `{"url":"`+u+`"}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("url %q: status = %d, want 400", u, rec.Code)
		}
	}
}
