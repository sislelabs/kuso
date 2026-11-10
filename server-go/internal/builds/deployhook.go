package builds

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A deploy hook is a secret URL that starts a build of one service: paste
// it into a repo's webhook settings or curl it from CI. It is how a push
// deploys on an install with no GitHub App.

const deployHookTokenKey = "TOKEN"

// DeployHookSecretName is the per-service Secret holding the hook token.
func DeployHookSecretName(project, service string) string {
	return project + "-" + service + "-deploy-hook"
}

// DeployHookToken returns the service's hook token, "" when the hook is
// not enabled.
func (s *Service) DeployHookToken(ctx context.Context, project, service string) (string, error) {
	sec, err := s.Kube.Clientset.CoreV1().Secrets(s.nsFor(ctx, project)).
		Get(ctx, DeployHookSecretName(project, service), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get deploy hook: %w", err)
	}
	return string(sec.Data[deployHookTokenKey]), nil
}

// EnsureDeployHook enables the hook and returns its token. An existing
// token is kept unless rotate is set, so the URL already pasted into a
// repo keeps working.
func (s *Service) EnsureDeployHook(ctx context.Context, project, service string, rotate bool) (string, error) {
	ns := s.nsFor(ctx, project)
	if _, err := s.Kube.GetKusoService(ctx, ns, project+"-"+service); err != nil {
		if apierrors.IsNotFound(err) {
			return "", fmt.Errorf("%w: service %s/%s", ErrNotFound, project, service)
		}
		return "", fmt.Errorf("get service: %w", err)
	}
	if !rotate {
		if tok, err := s.DeployHookToken(ctx, project, service); err != nil || tok != "" {
			return tok, err
		}
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate deploy hook token: %w", err)
	}
	tok := hex.EncodeToString(raw)
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      DeployHookSecretName(project, service),
			Namespace: ns,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by":   "kuso-server",
				"kuso.sislelabs.com/project":     project,
				"kuso.sislelabs.com/service":     service,
				"kuso.sislelabs.com/deploy-hook": "true",
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{deployHookTokenKey: []byte(tok)},
	}
	secrets := s.Kube.Clientset.CoreV1().Secrets(ns)
	if _, err := secrets.Create(ctx, sec, metav1.CreateOptions{}); apierrors.IsAlreadyExists(err) {
		if _, err := secrets.Update(ctx, sec, metav1.UpdateOptions{}); err != nil {
			return "", fmt.Errorf("rotate deploy hook: %w", err)
		}
	} else if err != nil {
		return "", fmt.Errorf("create deploy hook: %w", err)
	}
	return tok, nil
}

// DeleteDeployHook disables the hook. Deleting a hook that isn't enabled
// is not an error.
func (s *Service) DeleteDeployHook(ctx context.Context, project, service string) error {
	err := s.Kube.Clientset.CoreV1().Secrets(s.nsFor(ctx, project)).
		Delete(ctx, DeployHookSecretName(project, service), metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete deploy hook: %w", err)
	}
	return nil
}

// VerifyDeployHook reports whether presented is the service's hook token.
func (s *Service) VerifyDeployHook(ctx context.Context, project, service, presented string) bool {
	if presented == "" {
		return false
	}
	want, err := s.DeployHookToken(ctx, project, service)
	if err != nil || want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(want), []byte(presented)) == 1
}

// DeployedBranches lists the branches a push can deploy for this service:
// the branch of each of its persistent environments. Preview environments
// are excluded; they follow pull requests, not branch pushes.
func (s *Service) DeployedBranches(ctx context.Context, project, service string) ([]string, error) {
	proj, err := s.Kube.GetKusoProject(ctx, s.Namespace, project)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: project %s", ErrNotFound, project)
		}
		return nil, fmt.Errorf("get project: %w", err)
	}
	defaultBranch := "main"
	if proj.Spec.DefaultRepo != nil && proj.Spec.DefaultRepo.DefaultBranch != "" {
		defaultBranch = proj.Spec.DefaultRepo.DefaultBranch
	}
	envs, err := s.serviceEnvs(ctx, s.nsFor(ctx, project), project, service)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for i := range envs {
		if envs[i].Spec.Kind == "preview" {
			continue
		}
		if b := envBranch(&envs[i], defaultBranch); !seen[b] {
			seen[b] = true
			out = append(out, b)
		}
	}
	return out, nil
}

// CommitOnBranchChecker reports whether sha is a branch's head or one of
// its ancestors. The github.Client wired as RepoAccess implements it.
type CommitOnBranchChecker interface {
	CommitOnBranch(ctx context.Context, installationID int64, owner, repo, branch, sha string) (bool, error)
}

// VerifyHookRef refuses a deploy-hook commit that isn't on one of
// branches in the service's GitHub repo. The hook token is the only
// credential, so without this a leaked URL could deploy any commit
// GitHub serves for the repo, including ones that only exist on a fork's
// PR. Older commits on a tracked branch stay deployable (CI that deploys
// $GITHUB_SHA after a newer push landed). Repos the GitHub App can't see
// (non-GitHub hosts, uncovered public repos) can't be checked and pass.
func (s *Service) VerifyHookRef(ctx context.Context, project, service string, branches []string, sha string) error {
	checker, ok := s.RepoAccess.(CommitOnBranchChecker)
	if !ok || sha == "" || len(branches) == 0 {
		return nil
	}
	svcCR, err := s.Kube.GetOwnedService(ctx, s.nsFor(ctx, project), project, project+"-"+service)
	if err != nil {
		return nil // create() reports a missing service with the right status
	}
	proj, err := s.Kube.GetKusoProject(ctx, s.Namespace, project)
	if err != nil {
		return nil
	}
	repoURL := ""
	if svcCR.Spec.Repo != nil {
		repoURL = svcCR.Spec.Repo.URL
	}
	if repoURL == "" && proj.Spec.DefaultRepo != nil {
		repoURL = proj.Spec.DefaultRepo.URL
	}
	owner, repo := splitGithubURL(NormalizeRepoURL(repoURL))
	if owner == "" {
		return nil
	}
	installationID := githubInstallationID(proj, svcCR)
	if installationID == 0 && s.InstallResolver != nil {
		if id, err := s.InstallResolver.ResolveInstallationForRepo(ctx, owner, repo); err == nil {
			installationID = id
		}
	}
	if installationID == 0 {
		return nil
	}
	for _, b := range branches {
		on, err := checker.CommitOnBranch(ctx, installationID, owner, repo, b, sha)
		if err != nil {
			return fmt.Errorf("verify deploy-hook ref: %w", err)
		}
		if on {
			return nil
		}
	}
	return fmt.Errorf("%w: commit %s is not on %s of %s/%s", ErrInvalid, sha, strings.Join(branches, " or "), owner, repo)
}
