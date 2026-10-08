package projects

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuso/server/internal/kube"
)

func groupSrcSecret(name, project, val string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso", Labels: map[string]string{labelProject: project}},
		Data:       map[string][]byte{"API_KEY": []byte(val)},
	}
}

// A failed group create must not leave the copy of production's managed
// secret values behind.
func TestCreateEnvGroup_RollbackDeletesCopiedSecret(t *testing.T) {
	t.Parallel()
	s := fakeServiceWithSecrets(t, []runtime.Object{groupSrcSecret("alpha-web-secrets", "alpha", "prod")},
		seedProject("alpha", kube.KusoProjectSpec{BaseDomain: "apps.example.com"}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Port: 8080}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)
	s.Kube.Dynamic.(*dynamicfake.FakeDynamicClient).PrependReactor("create", "kusoenvironments", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("injected env create failure")
	})
	if _, err := s.CreateEnvGroup(context.Background(), "alpha", CreateEnvGroupRequest{Name: "qa"}); err == nil {
		t.Fatal("CreateEnvGroup succeeded, want injected failure")
	}
	if _, err := s.Kube.Clientset.CoreV1().Secrets("kuso").Get(context.Background(), "alpha-web-qa-secrets", metav1.GetOptions{}); err == nil {
		t.Fatal("copied managed secret survived rollback")
	}
}

// A leftover copy from an earlier attempt is refreshed, not silently reused.
func TestCreateEnvGroup_RefreshesLeftoverSecretCopy(t *testing.T) {
	t.Parallel()
	s := fakeServiceWithSecrets(t, []runtime.Object{
		groupSrcSecret("alpha-web-secrets", "alpha", "rotated"),
		groupSrcSecret("alpha-web-qa-secrets", "alpha", "stale"),
	},
		seedProject("alpha", kube.KusoProjectSpec{BaseDomain: "apps.example.com"}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Port: 8080}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)
	if _, err := s.CreateEnvGroup(context.Background(), "alpha", CreateEnvGroupRequest{Name: "qa"}); err != nil {
		t.Fatalf("CreateEnvGroup: %v", err)
	}
	if got := string(getSecret(t, s, "kuso", "alpha-web-qa-secrets").Data["API_KEY"]); got != "rotated" {
		t.Errorf("clone secret API_KEY = %q, want rotated", got)
	}
}
