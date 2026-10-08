package projects

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"kuso/server/internal/kube"
)

// Managed-secret names are raw concatenations, so the rename target can be
// a Secret another project owns. Rename must refuse, not overwrite it.
func TestRenameService_RefusesForeignSecretAtTarget(t *testing.T) {
	t.Parallel()
	own := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-secrets", Namespace: "kuso", Labels: map[string]string{kube.LabelProject: "alpha"}},
		Data:       map[string][]byte{"K": []byte("mine")},
	}
	foreign := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-b-c-secrets", Namespace: "kuso", Labels: map[string]string{kube.LabelProject: "alpha-b"}},
		Data:       map[string][]byte{"K": []byte("theirs")},
	}
	s := fakeServiceWithSecrets(t, []runtime.Object{own, foreign},
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)
	_, err := s.RenameService(context.Background(), "alpha", "web", "b-c")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("RenameService err = %v, want ErrConflict", err)
	}
	if got := string(getSecret(t, s, "kuso", "alpha-b-c-secrets").Data["K"]); got != "theirs" {
		t.Errorf("foreign secret overwritten: K = %q", got)
	}
	if _, gerr := s.GetService(context.Background(), "alpha", "web"); gerr != nil {
		t.Errorf("old service gone after refused rename: %v", gerr)
	}
}
