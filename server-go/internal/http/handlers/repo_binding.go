package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"kuso/server/internal/auth"
	"kuso/server/internal/github"
)

// requireRepoBound gates writes that point a service or project at a
// GitHub repo or App installation. The GitHub App is instance-wide and
// builds auto-resolve an installation token for any repo it can see, so a
// project-scoped editor naming an arbitrary repo would get it cloned and
// built (and readable through logs) without any GitHub access of their
// own. Such callers may only use repos and installations the project
// already references. Instance admins and instance editors
// (projects:create) pass: they already list every App repo through
// /api/github/repos to create projects. Non-GitHub hosts pass; no App
// token is ever minted for them.
func (h *ProjectsHandler) requireRepoBound(ctx context.Context, w http.ResponseWriter, project string, urls []string, installationIDs []int64) bool {
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return false
	}
	if auth.HasAny(claims.Permissions, auth.PermSettingsAdmin, auth.PermProjectsCreate) {
		return true
	}
	var wantRepos []string
	for _, u := range urls {
		if owner, name := github.ParseGithubRepoURL(u); owner != "" {
			wantRepos = append(wantRepos, strings.ToLower(owner+"/"+name))
		}
	}
	var wantInsts []int64
	for _, id := range installationIDs {
		if id != 0 {
			wantInsts = append(wantInsts, id)
		}
	}
	if len(wantRepos) == 0 && len(wantInsts) == 0 {
		return true
	}
	if h.Svc == nil {
		writeErr(w, http.StatusForbidden, "forbidden")
		return false
	}

	boundRepos := map[string]bool{}
	boundInsts := map[int64]bool{}
	addRepo := func(u string) {
		if owner, name := github.ParseGithubRepoURL(u); owner != "" {
			boundRepos[strings.ToLower(owner+"/"+name)] = true
		}
	}
	proj, err := h.Svc.Get(ctx, project)
	if err != nil {
		h.fail(w, "repo binding", err)
		return false
	}
	if proj.Spec.DefaultRepo != nil {
		addRepo(proj.Spec.DefaultRepo.URL)
	}
	if proj.Spec.GitHub != nil {
		boundInsts[proj.Spec.GitHub.InstallationID] = true
	}
	svcs, err := h.Svc.ListServices(ctx, project)
	if err != nil {
		h.fail(w, "repo binding", err)
		return false
	}
	for i := range svcs {
		if svcs[i].Spec.Repo != nil {
			addRepo(svcs[i].Spec.Repo.URL)
		}
		if svcs[i].Spec.Github != nil {
			boundInsts[svcs[i].Spec.Github.InstallationID] = true
		}
	}

	for _, r := range wantRepos {
		if !boundRepos[r] {
			writeErr(w, http.StatusForbidden, fmt.Sprintf(
				"github.com/%s isn't used by project %s yet; an instance admin or instance editor has to add it first", r, project))
			return false
		}
	}
	for _, id := range wantInsts {
		if !boundInsts[id] {
			writeErr(w, http.StatusForbidden, fmt.Sprintf(
				"GitHub App installation %d isn't used by project %s yet; an instance admin or instance editor has to set it", id, project))
			return false
		}
	}
	return true
}
