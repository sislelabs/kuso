package projects

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// TestPatchService_GitLabRepoStoresToken verifies the GitLab repo flow:
// patching a service with a GitLab repo URL + token stores the token in a
// per-service Secret (never on the CR) and points repo.TokenSecret at it.
func TestPatchService_GitLabRepoStoresToken(t *testing.T) {
	s := fakeServiceWithSecrets(t, nil,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)
	_, err := s.PatchService(context.Background(), "alpha", "web", PatchServiceRequest{
		Repo: &PatchRepoRequest{
			URL:   "https://gitlab.com/group/app.git",
			Token: "glpat-secret-token",
		},
	})
	if err != nil {
		t.Fatalf("PatchService: %v", err)
	}

	svc, _ := s.GetService(context.Background(), "alpha", "web")
	if svc.Spec.Repo == nil {
		t.Fatal("repo not set")
	}
	// Provider inferred as gitlab; TokenSecret points at the per-service Secret.
	if got := kube.RepoProviderForRef(svc.Spec.Repo); got != kube.ProviderGitLab {
		t.Errorf("provider = %q, want gitlab", got)
	}
	wantSec := repoTokenSecretName("alpha", "web")
	if svc.Spec.Repo.TokenSecret != wantSec {
		t.Errorf("repo.TokenSecret = %q, want %q", svc.Spec.Repo.TokenSecret, wantSec)
	}
	// The raw token must NOT be on the CR anywhere.
	if svc.Spec.Repo.URL == "glpat-secret-token" {
		t.Fatal("token leaked onto CR")
	}
	// The token IS in the Secret under the standard key.
	sec, err := s.Kube.Clientset.CoreV1().Secrets("kuso").Get(context.Background(), wantSec, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("token secret not created: %v", err)
	}
	if got := string(sec.Data[kube.RepoTokenSecretKey]); got != "glpat-secret-token" {
		t.Errorf("stored token = %q, want glpat-secret-token", got)
	}
}

// TestPatchService_GitLabBranchEditKeepsToken: editing the repo (e.g. branch)
// WITHOUT supplying a new token preserves the existing TokenSecret.
func TestPatchService_GitLabBranchEditKeepsToken(t *testing.T) {
	s := fakeServiceWithSecrets(t, nil,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{
			Project: "alpha",
			Repo: &kube.KusoRepoRef{
				URL:         "https://gitlab.com/group/app.git",
				Provider:    "gitlab",
				TokenSecret: "alpha-web-repo-token",
			},
		}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)
	_, err := s.PatchService(context.Background(), "alpha", "web", PatchServiceRequest{
		Repo: &PatchRepoRequest{URL: "https://gitlab.com/group/app.git", Branch: "develop"},
	})
	if err != nil {
		t.Fatalf("PatchService: %v", err)
	}
	svc, _ := s.GetService(context.Background(), "alpha", "web")
	if svc.Spec.Repo.TokenSecret != "alpha-web-repo-token" {
		t.Errorf("branch edit dropped the token secret: %q", svc.Spec.Repo.TokenSecret)
	}
	if svc.Spec.Repo.DefaultBranch != "develop" {
		t.Errorf("branch not updated: %q", svc.Spec.Repo.DefaultBranch)
	}
}
