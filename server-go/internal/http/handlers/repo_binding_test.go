package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/auth"
	"kuso/server/internal/kube"
)

type boundReposSvc struct {
	ProjectsAPI
}

func (boundReposSvc) Get(context.Context, string) (*kube.KusoProject, error) {
	return &kube.KusoProject{
		ObjectMeta: metav1.ObjectMeta{Name: "shop"},
		Spec: kube.KusoProjectSpec{
			DefaultRepo: &kube.KusoRepoRef{URL: "https://github.com/acme/shop"},
			GitHub:      &kube.KusoProjectGithubSpec{InstallationID: 7},
		},
	}, nil
}

func (boundReposSvc) ListServices(context.Context, string) ([]kube.KusoService, error) {
	return []kube.KusoService{{Spec: kube.KusoServiceSpec{Repo: &kube.KusoRepoRef{URL: "git@github.com:acme/worker.git"}}}}, nil
}

// AUTHZ-01: a project-scoped editor may only point services at GitHub
// repos (and App installations) already bound to the project; the shared
// App would otherwise clone any repo it can see on their behalf.
func TestRequireRepoBound(t *testing.T) {
	t.Parallel()
	h := &ProjectsHandler{Svc: boundReposSvc{}}
	editor := &auth.Claims{UserID: "ed"}
	creator := &auth.Claims{UserID: "ie", Permissions: []string{string(auth.PermProjectsCreate)}}

	cases := []struct {
		name   string
		claims *auth.Claims
		urls   []string
		insts  []int64
		want   bool
	}{
		{"project default repo", editor, []string{"https://github.com/ACME/shop.git"}, nil, true},
		{"sibling service repo", editor, []string{"https://github.com/acme/worker"}, nil, true},
		{"non-github host", editor, []string{"https://gitlab.com/acme/other"}, nil, true},
		{"bound installation", editor, nil, []int64{7}, true},
		{"new github repo", editor, []string{"https://github.com/acme/infra-secrets"}, nil, false},
		{"foreign installation", editor, nil, []int64{99}, false},
		{"instance editor, new repo", creator, []string{"https://github.com/acme/infra-secrets"}, []int64{99}, true},
	}
	for _, c := range cases {
		ctx := auth.WithClaimsForTest(context.Background(), c.claims)
		rec := httptest.NewRecorder()
		got := h.requireRepoBound(ctx, rec, "shop", c.urls, c.insts)
		if got != c.want {
			t.Errorf("%s: allowed = %v, want %v", c.name, got, c.want)
		}
		if !got && rec.Code != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", c.name, rec.Code)
		}
	}
}
