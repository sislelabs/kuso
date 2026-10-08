package projects

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// A token supplied with a GitHub URL is stored the same way a GitLab one
// is: that is how a private GitHub repo deploys with no GitHub App.
func TestPatchService_GitHubRepoStoresToken(t *testing.T) {
	s := fakeServiceWithSecrets(t, nil,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)
	_, err := s.PatchService(context.Background(), "alpha", "web", PatchServiceRequest{
		Repo: &PatchRepoRequest{URL: "https://github.com/acme/private.git", Token: "github_pat_abc"},
	})
	if err != nil {
		t.Fatalf("PatchService: %v", err)
	}
	svc, _ := s.GetService(context.Background(), "alpha", "web")
	wantSec := repoTokenSecretName("alpha", "web")
	if svc.Spec.Repo == nil || svc.Spec.Repo.TokenSecret != wantSec {
		t.Fatalf("repo.TokenSecret = %+v, want %q", svc.Spec.Repo, wantSec)
	}
	sec, err := s.Kube.Clientset.CoreV1().Secrets("kuso").Get(context.Background(), wantSec, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("token secret not created: %v", err)
	}
	if got := string(sec.Data[kube.RepoTokenSecretKey]); got != "github_pat_abc" {
		t.Errorf("stored token = %q", got)
	}
}

// Moving a service to another git host without a new token must drop the
// old reference: git offers stored credentials to whatever host answers 401.
func TestPatchService_HostChangeDropsStoredToken(t *testing.T) {
	s := fakeServiceWithSecrets(t, nil,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{
			Project: "alpha",
			Repo:    &kube.KusoRepoRef{URL: "https://github.com/acme/private.git", TokenSecret: "alpha-web-repo-token"},
		}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)
	_, err := s.PatchService(context.Background(), "alpha", "web", PatchServiceRequest{
		Repo: &PatchRepoRequest{URL: "https://git.example.org/acme/private.git"},
	})
	if err != nil {
		t.Fatalf("PatchService: %v", err)
	}
	svc, _ := s.GetService(context.Background(), "alpha", "web")
	if svc.Spec.Repo.TokenSecret != "" {
		t.Errorf("token reference survived a host change: %q", svc.Spec.Repo.TokenSecret)
	}
}

// Same host, different repo or branch: the token stays.
func TestPatchService_SameHostEditKeepsStoredToken(t *testing.T) {
	s := fakeServiceWithSecrets(t, nil,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{
			Project: "alpha",
			Repo:    &kube.KusoRepoRef{URL: "https://github.com/acme/private.git", TokenSecret: "alpha-web-repo-token"},
		}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)
	_, err := s.PatchService(context.Background(), "alpha", "web", PatchServiceRequest{
		Repo: &PatchRepoRequest{URL: "https://github.com/acme/private.git", Branch: "develop"},
	})
	if err != nil {
		t.Fatalf("PatchService: %v", err)
	}
	svc, _ := s.GetService(context.Background(), "alpha", "web")
	if svc.Spec.Repo.TokenSecret != "alpha-web-repo-token" {
		t.Errorf("branch edit dropped the token: %q", svc.Spec.Repo.TokenSecret)
	}
}
