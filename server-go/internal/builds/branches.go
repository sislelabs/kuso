package builds

import (
	"context"
	"fmt"
	"time"

	"kuso/server/internal/kube"
)

// RepoBranchesMax caps how many branch names one listing returns.
const RepoBranchesMax = 1000

// RemoteBranchLister lists a repo's branches straight from the git
// remote. *gitremote.Inspector implements it.
type RemoteBranchLister interface {
	BranchNames(ctx context.Context, repoURL, token string) (defaultBranch string, branches []string, err error)
}

// RepoBranches is what ListRepoBranches found.
type RepoBranches struct {
	DefaultBranch string   `json:"defaultBranch"`
	Branches      []string `json:"branches"`
	Truncated     bool     `json:"truncated,omitempty"`
}

// ListRepoBranches lists the branches of the service's repo with the same
// credentials a build clones with: a repo-scoped GitHub App token when an
// installation reaches the repo, else the service's stored token, else
// anonymous. It never writes anything.
func (s *Service) ListRepoBranches(ctx context.Context, project, service string) (*RepoBranches, error) {
	ns := s.nsFor(ctx, project)
	svcCR, err := s.Kube.GetOwnedService(ctx, ns, project, project+"-"+service)
	if kube.IsNotFoundOrNotOwned(err) {
		return nil, fmt.Errorf("%w: service %s/%s", ErrNotFound, project, service)
	}
	if err != nil {
		return nil, fmt.Errorf("get service: %w", err)
	}
	proj, err := s.Kube.GetKusoProject(ctx, s.Namespace, project)
	if err != nil {
		return nil, fmt.Errorf("get project: %w", err)
	}
	repoURL := ""
	if svcCR.Spec.Repo != nil {
		repoURL = svcCR.Spec.Repo.URL
	}
	if repoURL == "" && proj.Spec.DefaultRepo != nil {
		repoURL = proj.Spec.DefaultRepo.URL
	}
	if repoURL == "" || svcCR.Spec.Runtime == "image" {
		return nil, fmt.Errorf("%w: this service doesn't build from a git repo", ErrInvalid)
	}
	if err := ValidateRepoURL(repoURL); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalid, err.Error())
	}
	repoURL = NormalizeRepoURL(repoURL)
	lister, ok := s.Remote.(RemoteBranchLister)
	if !ok {
		return nil, fmt.Errorf("%w: no way to list branches is configured", ErrRepoUnreachable)
	}

	rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	token := ""
	if svcCR.Spec.Repo != nil && svcCR.Spec.Repo.TokenSecret != "" {
		if t, terr := s.readRepoToken(rctx, ns, svcCR.Spec.Repo.TokenSecret); terr == nil {
			token = t
		}
	}
	if owner, repo := splitGithubURL(repoURL); owner != "" {
		installationID := githubInstallationID(proj, svcCR)
		if installationID == 0 && s.InstallResolver != nil {
			if id, rerr := s.InstallResolver.ResolveInstallationForRepo(rctx, owner, repo); rerr == nil && id > 0 {
				installationID = id
			}
		}
		if rs, ok := s.Tokens.(RepoScopedTokenMinter); ok && installationID > 0 {
			if t, merr := rs.MintRepoScopedToken(rctx, installationID, owner, repo); merr == nil {
				token = t
			}
		}
	}

	def, names, err := lister.BranchNames(rctx, repoURL, token)
	if err != nil {
		return nil, fmt.Errorf("%w: list branches: %v", ErrRepoUnreachable, err)
	}
	out := &RepoBranches{DefaultBranch: def, Branches: names}
	if out.Branches == nil {
		out.Branches = []string{}
	}
	if len(out.Branches) > RepoBranchesMax {
		out.Branches, out.Truncated = out.Branches[:RepoBranchesMax], true
	}
	return out, nil
}
