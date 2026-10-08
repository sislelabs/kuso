package handlers

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/builds"
	"kuso/server/internal/kube"
)

const hookSHA = "46fe4156ba59e4cd0afd264febce9890068f253d"

type fakeHookBuilds struct {
	token    string
	branches []string
	created  []builds.CreateBuildRequest
}

func (f *fakeHookBuilds) VerifyDeployHook(_ context.Context, _, _, presented string) bool {
	return f.token != "" && presented == f.token
}

func (f *fakeHookBuilds) DeployedBranches(context.Context, string, string) ([]string, error) {
	return f.branches, nil
}

func (f *fakeHookBuilds) CreateWithOutcome(_ context.Context, _, _ string, req builds.CreateBuildRequest) (builds.CreateOutcome, error) {
	f.created = append(f.created, req)
	return builds.CreateOutcome{Build: &kube.KusoBuild{}}, nil
}

func hookServer(f *fakeHookBuilds) http.Handler {
	r := chi.NewRouter()
	(&DeployHookHandler{Hooks: f, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}).MountPublic(r)
	return r
}

func postHook(h http.Handler, path, event, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if event != "" {
		req.Header.Set("X-GitHub-Event", event)
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestDeployHook_PlainPostBuildsTheDefaultBranch(t *testing.T) {
	f := &fakeHookBuilds{token: "tok", branches: []string{"main"}}
	rec := postHook(hookServer(f), "/api/hooks/deploy/shop/api/tok", "", "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if len(f.created) != 1 || f.created[0].Branch != "" || f.created[0].Ref != "" {
		t.Errorf("created = %+v, want one build with no branch/ref override", f.created)
	}
}

func TestDeployHook_WrongTokenIs404AndBuildsNothing(t *testing.T) {
	f := &fakeHookBuilds{token: "tok", branches: []string{"main"}}
	for _, path := range []string{"/api/hooks/deploy/shop/api/nope", "/api/hooks/deploy/shop/api/"} {
		rec := postHook(hookServer(f), path, "", "")
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", path, rec.Code)
		}
	}
	if len(f.created) != 0 {
		t.Errorf("a bad token started %d build(s)", len(f.created))
	}
}

func TestDeployHook_GitHubPushToDeployedBranchBuildsThatCommit(t *testing.T) {
	f := &fakeHookBuilds{token: "tok", branches: []string{"main", "develop"}}
	body := `{"ref":"refs/heads/develop","after":"` + hookSHA + `","deleted":false}`
	rec := postHook(hookServer(f), "/api/hooks/deploy/shop/api/tok", "push", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if len(f.created) != 1 || f.created[0].Branch != "develop" || f.created[0].Ref != hookSHA {
		t.Errorf("created = %+v, want develop@%s", f.created, hookSHA)
	}
}

func TestDeployHook_GitHubEventsThatMustNotBuild(t *testing.T) {
	cases := map[string]struct{ event, body string }{
		"ping":           {"ping", `{"zen":"hi"}`},
		"other branch":   {"push", `{"ref":"refs/heads/feature","after":"` + hookSHA + `"}`},
		"tag push":       {"push", `{"ref":"refs/tags/v1","after":"` + hookSHA + `"}`},
		"branch deleted": {"push", `{"ref":"refs/heads/main","after":"0000000000000000000000000000000000000000","deleted":true}`},
		"other event":    {"issues", `{}`},
	}
	for name, c := range cases {
		f := &fakeHookBuilds{token: "tok", branches: []string{"main"}}
		rec := postHook(hookServer(f), "/api/hooks/deploy/shop/api/tok", c.event, c.body)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200 (a non-2xx makes GitHub mark the hook failed)", name, rec.Code)
		}
		var out struct {
			Skipped string `json:"skipped"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		if out.Skipped == "" {
			t.Errorf("%s: response %s should say why it was skipped", name, rec.Body)
		}
		if len(f.created) != 0 {
			t.Errorf("%s: started %d build(s)", name, len(f.created))
		}
	}
}

func TestDeployHook_QueryParamsPickBranchAndCommit(t *testing.T) {
	f := &fakeHookBuilds{token: "tok", branches: []string{"main"}}
	rec := postHook(hookServer(f), "/api/hooks/deploy/shop/api/tok?branch=main&ref="+hookSHA, "", "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if len(f.created) != 1 || f.created[0].Branch != "main" || f.created[0].Ref != hookSHA {
		t.Errorf("created = %+v", f.created)
	}
}
