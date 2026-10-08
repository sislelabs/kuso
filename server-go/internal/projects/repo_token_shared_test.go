package projects

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"kuso/server/internal/kube"
)

// An env-group clone inherits spec.repo.tokenSecret from its source.
// Deleting the group must not delete the token production still clones with.
func TestDeleteEnvGroup_KeepsSourceRepoToken(t *testing.T) {
	t.Parallel()
	tok := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-repo-token", Namespace: "kuso"},
		Data:       map[string][]byte{kube.RepoTokenSecretKey: []byte("glpat-x")},
	}
	s := fakeServiceWithSecrets(t, []runtime.Object{tok},
		seedProject("alpha", kube.KusoProjectSpec{BaseDomain: "apps.example.com"}),
		seedService("alpha", "web", kube.KusoServiceSpec{
			Project: "alpha",
			Port:    8080,
			Repo:    &kube.KusoRepoRef{URL: "https://gitlab.com/g/app.git", TokenSecret: "alpha-web-repo-token"},
		}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)
	ctx := context.Background()
	if _, err := s.CreateEnvGroup(ctx, "alpha", CreateEnvGroupRequest{Name: "qa"}); err != nil {
		t.Fatalf("CreateEnvGroup: %v", err)
	}
	if err := s.DeleteEnvGroup(ctx, "alpha", "qa"); err != nil {
		t.Fatalf("DeleteEnvGroup: %v", err)
	}
	if _, err := s.Kube.Clientset.CoreV1().Secrets("kuso").Get(ctx, "alpha-web-repo-token", metav1.GetOptions{}); err != nil {
		t.Fatalf("production repo token deleted by env-group delete: %v", err)
	}
}

// A renamed service keeps its token under the old name; deleting it with no
// other reference still reclaims that token.
func TestDeleteService_ReclaimsUnsharedRenamedToken(t *testing.T) {
	t.Parallel()
	tok := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-repo-token", Namespace: "kuso"},
		Data:       map[string][]byte{kube.RepoTokenSecretKey: []byte("glpat-x")},
	}
	s := fakeServiceWithSecrets(t, []runtime.Object{tok},
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "api", kube.KusoServiceSpec{
			Project: "alpha",
			Repo:    &kube.KusoRepoRef{URL: "https://gitlab.com/g/app.git", TokenSecret: "alpha-web-repo-token"},
		}),
	)
	ctx := context.Background()
	if err := s.DeleteService(ctx, "alpha", "api"); err != nil {
		t.Fatalf("DeleteService: %v", err)
	}
	if _, err := s.Kube.Clientset.CoreV1().Secrets("kuso").Get(ctx, "alpha-web-repo-token", metav1.GetOptions{}); err == nil {
		t.Fatal("unreferenced renamed-service token survived delete")
	}
}
