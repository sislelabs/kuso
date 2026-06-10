package incidents

import (
	"context"
	"fmt"
	"strings"

	"kuso/server/internal/db"
	"kuso/server/internal/kube"
)

// tokenMinter mints a short-lived GitHub App installation token. Satisfied
// by *github.Client.MintInstallationToken (injected so the resolver is
// testable without a real GitHub App).
type tokenMinter interface {
	MintInstallationToken(ctx context.Context, installationID int64) (string, error)
}

// GithubRepoResolver resolves an incident's repo coordinates + a push token
// for the implement phase. It reads the project's KusoService CR (repo URL +
// installation id), falling back to the project-level GitHub installation id,
// and mints a token. Implements incidents.RepoResolver.
type GithubRepoResolver struct {
	Kube      *kube.Client
	Tokens    tokenMinter
	Namespace string // home namespace where the CRs live (e.g. "kuso")
}

// Resolve implements RepoResolver.
func (g *GithubRepoResolver) Resolve(ctx context.Context, in db.Incident) (RepoInfo, error) {
	if g.Kube == nil {
		return RepoInfo{}, fmt.Errorf("incidents: repo resolver has no kube client")
	}
	if in.Project == "" || in.Service == "" {
		// node.unreachable and project-less incidents have no repo to fix.
		return RepoInfo{}, fmt.Errorf("incident %s has no project/service repo", in.ID)
	}
	ns := g.Namespace
	if ns == "" {
		ns = "kuso"
	}
	svcCR := in.Project + "-" + in.Service
	svc, err := g.Kube.GetKusoService(ctx, ns, svcCR)
	if err != nil {
		return RepoInfo{}, fmt.Errorf("get service CR %s: %w", svcCR, err)
	}
	if svc.Spec.Repo == nil || svc.Spec.Repo.URL == "" {
		return RepoInfo{}, fmt.Errorf("service %s has no repo configured", svcCR)
	}
	owner, name, err := parseGitHubURL(svc.Spec.Repo.URL)
	if err != nil {
		return RepoInfo{}, err
	}
	branch := svc.Spec.Repo.DefaultBranch
	if branch == "" {
		branch = "main"
	}

	// Installation id: service-level overrides project-level.
	instID := int64(0)
	if svc.Spec.Github != nil {
		instID = svc.Spec.Github.InstallationID
	}
	if instID == 0 {
		if proj, perr := g.Kube.GetKusoProject(ctx, ns, in.Project); perr == nil && proj.Spec.GitHub != nil {
			instID = proj.Spec.GitHub.InstallationID
		}
	}

	info := RepoInfo{Owner: owner, Name: name, DefaultBranch: branch}
	if g.Tokens != nil && instID > 0 {
		tok, terr := g.Tokens.MintInstallationToken(ctx, instID)
		if terr != nil {
			return info, fmt.Errorf("mint git token (installation %d): %w", instID, terr)
		}
		info.GitToken = tok
	}
	return info, nil
}

// parseGitHubURL extracts owner + repo from a GitHub URL. Tolerates the
// common forms: https://github.com/o/r(.git), git@github.com:o/r(.git).
func parseGitHubURL(url string) (owner, repo string, err error) {
	s := strings.TrimSpace(url)
	s = strings.TrimSuffix(s, ".git")
	// Normalize scp-like git@host:owner/repo to host/owner/repo.
	if i := strings.Index(s, "@"); i >= 0 {
		s = s[i+1:]
		s = strings.Replace(s, ":", "/", 1)
	}
	// Strip scheme.
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	// Now host/owner/repo... — take the two segments after the host.
	parts := strings.Split(strings.Trim(s, "/"), "/")
	if len(parts) < 3 {
		return "", "", fmt.Errorf("not a github owner/repo URL: %q", url)
	}
	// Defence in depth: only github.com. The agent's git token is a GitHub
	// App installation token; we never hand it to a non-github host even if
	// a KusoService CR was misconfigured (or tampered) with another host.
	if !strings.EqualFold(parts[0], "github.com") {
		return "", "", fmt.Errorf("only github.com repos are supported, got host %q", parts[0])
	}
	owner, repo = parts[1], parts[2]
	if owner == "" || repo == "" {
		return "", "", fmt.Errorf("empty owner/repo in URL: %q", url)
	}
	return owner, repo, nil
}
