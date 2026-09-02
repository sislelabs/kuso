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

// Deleting a native addon keeps its conn secret on purpose (resource-policy:
// keep, so a delete+re-add over the surviving PVC reuses the password). An
// external addon has no PVC and no data — the only thing kuso ever held was
// credentials, and after delete they must not linger in the namespace.
//
// Two secrets are involved: the mirrored <addon>-conn kuso writes for envFrom,
// and the source <addon>-external kuso created from --set. The source is only
// removed when kuso made it (label external-source=true); a Secret the user
// adopted with --secret is theirs and stays.
func TestDelete_ExternalRemovesCredentialSecrets(t *testing.T) {
	t.Parallel()
	s := fakeServiceWithSecrets(t, seedProj("tickero"))

	if _, err := s.Add(context.Background(), "tickero", CreateAddonRequest{
		Name: "psdb", Kind: "postgres",
		ExternalCredentials: map[string]string{"DATABASE_URL": "postgres://u:pw@h:5432/d"},
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	for _, n := range []string{"tickero-psdb-external", "tickero-psdb-conn"} {
		if _, err := s.Kube.Clientset.CoreV1().Secrets("kuso").Get(context.Background(), n, metav1.GetOptions{}); err != nil {
			t.Fatalf("precondition: %s should exist: %v", n, err)
		}
	}

	if err := s.Delete(context.Background(), "tickero", "psdb"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	for _, n := range []string{"tickero-psdb-external", "tickero-psdb-conn"} {
		_, err := s.Kube.Clientset.CoreV1().Secrets("kuso").Get(context.Background(), n, metav1.GetOptions{})
		if !apierrors.IsNotFound(err) {
			t.Errorf("%s survived the delete (err=%v) — the provider's credentials are orphaned in the namespace", n, err)
		}
	}
}

func TestDelete_ExternalKeepsUserSuppliedSecret(t *testing.T) {
	t.Parallel()
	s := fakeServiceWithSecrets(t, seedProj("tickero"))

	// A Secret the user created themselves and adopted with --secret.
	if _, err := s.Kube.Clientset.CoreV1().Secrets("kuso").Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "my-neon-creds", Namespace: "kuso"},
		Data:       map[string][]byte{"DATABASE_URL": []byte("postgres://u:pw@h:5432/d")},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(context.Background(), "tickero", CreateAddonRequest{
		Name: "neon", Kind: "postgres",
		External: &kube.KusoAddonExternal{SecretName: "my-neon-creds"},
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if err := s.Delete(context.Background(), "tickero", "neon"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := s.Kube.Clientset.CoreV1().Secrets("kuso").Get(context.Background(), "my-neon-creds", metav1.GetOptions{}); err != nil {
		t.Errorf("deleted a Secret the user supplied and still owns: %v", err)
	}
	if _, err := s.Kube.Clientset.CoreV1().Secrets("kuso").Get(context.Background(), "tickero-neon-conn", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("kuso's own mirrored conn secret survived: %v", err)
	}
}

// Rotating a managed provider's password had no supported path: connect-external
// refuses an existing addon (409) and resync-external only re-copies whatever the
// source Secret already holds. The only way in was kubectl patch on the Secret.
func TestResyncExternal_UpdatesCredentials(t *testing.T) {
	t.Parallel()
	s := fakeServiceWithSecrets(t, seedProj("tickero"))

	if _, err := s.Add(context.Background(), "tickero", CreateAddonRequest{
		Name: "psdb", Kind: "postgres",
		ExternalCredentials: map[string]string{
			"DATABASE_URL":      "postgres://u:OLD@h:5432/d",
			"POSTGRES_PASSWORD": "OLD",
			"POSTGRES_USER":     "u",
		},
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	err := s.ResyncExternal(context.Background(), "tickero", "psdb", map[string]string{
		"DATABASE_URL":      "postgres://u:NEW@h:5432/d",
		"POSTGRES_PASSWORD": "NEW",
	})
	if err != nil {
		t.Fatalf("ResyncExternal: %v", err)
	}

	conn, err := s.Kube.Clientset.CoreV1().Secrets("kuso").Get(context.Background(), "tickero-psdb-conn", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(conn.Data["POSTGRES_PASSWORD"]); got != "NEW" {
		t.Errorf("conn POSTGRES_PASSWORD = %q, want NEW — the rotated value never reached services", got)
	}
	// Keys not mentioned must survive: a rotation touches the password, not
	// the whole credential set.
	if got := string(conn.Data["POSTGRES_USER"]); got != "u" {
		t.Errorf("conn POSTGRES_USER = %q, want u (unrelated key was dropped by the update)", got)
	}
}

// A Secret the user adopted with --secret is theirs; kuso must not rewrite it
// behind their back.
func TestResyncExternal_RefusesToEditUserSecret(t *testing.T) {
	t.Parallel()
	s := fakeServiceWithSecrets(t, seedProj("tickero"))

	if _, err := s.Kube.Clientset.CoreV1().Secrets("kuso").Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "my-neon-creds", Namespace: "kuso"},
		Data:       map[string][]byte{"DATABASE_URL": []byte("postgres://u:pw@h:5432/d")},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(context.Background(), "tickero", CreateAddonRequest{
		Name: "neon", Kind: "postgres",
		External: &kube.KusoAddonExternal{SecretName: "my-neon-creds"},
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	err := s.ResyncExternal(context.Background(), "tickero", "neon", map[string]string{"DATABASE_URL": "x"})
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("got %v, want ErrInvalid — kuso should not rewrite a user-owned Secret", err)
	}
	// Plain resync (no credentials) still works on a user Secret.
	if err := s.ResyncExternal(context.Background(), "tickero", "neon", nil); err != nil {
		t.Errorf("plain resync on user secret: %v", err)
	}
}
