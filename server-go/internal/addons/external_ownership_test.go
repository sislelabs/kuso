package addons

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

func seedSecret(t *testing.T, s *Service, name string, labels map[string]string, data map[string]string) {
	t.Helper()
	d := map[string][]byte{}
	for k, v := range data {
		d[k] = []byte(v)
	}
	if _, err := s.Kube.Clientset.CoreV1().Secrets("kuso").Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso", Labels: labels},
		Data:       d,
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}

// Projects share the home namespace with the platform's own Secrets and every
// other project's credentials, so external.secretName must not adopt a Secret
// the addon can't prove it owns.
func TestAdd_ExternalRefusesSecretNotOwnedByAddon(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		secret string
		labels map[string]string
	}{
		{"platform secret", "kuso-server-secrets", nil},
		{"another project's kuso-created source", "beta-psdb-external", map[string]string{
			"kuso.sislelabs.com/external-source": "true",
			"kuso.sislelabs.com/addon":           "beta-psdb",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := fakeServiceWithSecrets(t, seedProj("alpha"))
			seedSecret(t, s, tc.secret, tc.labels, map[string]string{"JWT_SECRET": "platform-signing-key"})

			_, err := s.Add(context.Background(), "alpha", CreateAddonRequest{
				Name: "x", Kind: "redis",
				External: &kube.KusoAddonExternal{SecretName: tc.secret},
			})
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("Add adopting %s: got %v, want ErrInvalid", tc.secret, err)
			}
			if _, err := s.Kube.Clientset.CoreV1().Secrets("kuso").Get(context.Background(), "alpha-x-conn", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
				t.Errorf("alpha-x-conn was written (err=%v): %s's data leaked into alpha", err, tc.secret)
			}
		})
	}
}

// An addon CR that already points at another project's source Secret (created
// before adoption was checked, or by hand) must not be able to rewrite or
// delete that Secret through resync-external --set or addon delete.
func TestExternal_CannotWriteOrDeleteAnotherAddonsSource(t *testing.T) {
	t.Parallel()
	s := fakeServiceWithSecrets(t,
		seedProj("alpha"),
		seedAddonSpec("alpha", "steal", kube.KusoAddonSpec{
			Kind:     "postgres",
			External: &kube.KusoAddonExternal{SecretName: "beta-psdb-external"},
		}),
	)
	seedSecret(t, s, "beta-psdb-external", map[string]string{
		"kuso.sislelabs.com/external-source": "true",
		"kuso.sislelabs.com/addon":           "beta-psdb",
	}, map[string]string{"DATABASE_URL": "postgres://u:pw@beta-db/beta"})

	err := s.ResyncExternal(context.Background(), "alpha", "steal", map[string]string{
		"DATABASE_URL": "postgres://x:y@attacker.example/beta",
	})
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("ResyncExternal --set on beta's source: got %v, want ErrInvalid", err)
	}
	src, err := s.Kube.Clientset.CoreV1().Secrets("kuso").Get(context.Background(), "beta-psdb-external", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(src.Data["DATABASE_URL"]); got != "postgres://u:pw@beta-db/beta" {
		t.Errorf("beta-psdb-external DATABASE_URL = %q — alpha rewrote beta's credentials", got)
	}

	if err := s.Delete(context.Background(), "alpha", "steal"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Kube.Clientset.CoreV1().Secrets("kuso").Get(context.Background(), "beta-psdb-external", metav1.GetOptions{}); err != nil {
		t.Errorf("alpha's addon delete removed beta's source Secret: %v", err)
	}
}
