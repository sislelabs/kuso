package addons

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// fakeServiceWithSecrets is fakeService plus a typed clientset. The shared
// harness wires only the dynamic client, which is enough for CR-only tests
// but panics the moment a code path touches core/v1 Secrets.
func fakeServiceWithSecrets(t *testing.T, seeds ...seed) *Service {
	t.Helper()
	s := fakeService(t, seeds...)
	s.Kube.Clientset = kubefake.NewSimpleClientset()
	return s
}

// connect-external mirrors an existing kube Secret, which left users with no
// supported way to produce that Secret in the first place — `kuso secret set`
// writes a service-scoped env var, not a namespace Secret, so the documented
// path bottomed out at raw kubectl. ExternalCredentials closes that: hand the
// DSN (and any extra keys) to the API and it creates the source Secret, then
// mirrors it exactly as before.

func TestAdd_ExternalCredentials_CreatesSourceSecret(t *testing.T) {
	t.Parallel()
	s := fakeServiceWithSecrets(t, seedProj("alpha"))

	_, err := s.Add(context.Background(), "alpha", CreateAddonRequest{
		Name: "psdb",
		Kind: "postgres",
		ExternalCredentials: map[string]string{
			"DATABASE_URL":      "postgres://u:p@ext.example.com:5432/db?sslmode=require",
			"POSTGRES_HOST":     "ext.example.com",
			"POSTGRES_USER":     "u",
			"POSTGRES_PASSWORD": "p",
		},
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	// The generated source Secret holds what the caller supplied...
	src, err := s.Kube.Clientset.CoreV1().Secrets("kuso").
		Get(context.Background(), "alpha-psdb-external", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("source secret not created: %v", err)
	}
	if got := string(src.Data["POSTGRES_HOST"]); got != "ext.example.com" {
		t.Errorf("source secret POSTGRES_HOST = %q, want ext.example.com", got)
	}

	// ...and it is mirrored into the conn secret services actually envFrom:.
	conn, err := s.Kube.Clientset.CoreV1().Secrets("kuso").
		Get(context.Background(), "alpha-psdb-conn", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("conn secret not mirrored: %v", err)
	}
	if got := string(conn.Data["DATABASE_URL"]); got == "" {
		t.Error("conn secret is missing DATABASE_URL")
	}
}

func TestAdd_ExternalCredentials_RecordsSecretNameOnSpec(t *testing.T) {
	t.Parallel()
	s := fakeServiceWithSecrets(t, seedProj("alpha"))

	if _, err := s.Add(context.Background(), "alpha", CreateAddonRequest{
		Name:                "psdb",
		Kind:                "postgres",
		ExternalCredentials: map[string]string{"DATABASE_URL": "postgres://u:p@h:5432/d"},
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	addon, err := s.Kube.GetKusoAddon(context.Background(), "kuso", "alpha-psdb")
	if err != nil {
		t.Fatalf("get addon: %v", err)
	}
	if addon.Spec.External == nil || addon.Spec.External.SecretName != "alpha-psdb-external" {
		t.Errorf("spec.external = %+v, want secretName alpha-psdb-external — "+
			"without it resync-external and the render gate can't find the source",
			addon.Spec.External)
	}
}

// Supplying both is ambiguous: one names a Secret to adopt, the other asks to
// create one. Refuse rather than silently pick.
func TestAdd_ExternalCredentials_ConflictsWithSecretName(t *testing.T) {
	t.Parallel()
	s := fakeServiceWithSecrets(t, seedProj("alpha"))

	_, err := s.Add(context.Background(), "alpha", CreateAddonRequest{
		Name:                "psdb",
		Kind:                "postgres",
		External:            &kube.KusoAddonExternal{SecretName: "someone-elses-secret"},
		ExternalCredentials: map[string]string{"DATABASE_URL": "postgres://u:p@h:5432/d"},
	})
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("got %v, want ErrInvalid for external + externalCredentials", err)
	}
}

func TestAdd_ExternalCredentials_RequiresConnectionURL(t *testing.T) {
	t.Parallel()
	s := fakeServiceWithSecrets(t, seedProj("alpha"))

	_, err := s.Add(context.Background(), "alpha", CreateAddonRequest{
		Name:                "psdb",
		Kind:                "postgres",
		ExternalCredentials: map[string]string{"POSTGRES_HOST": "ext.example.com"},
	})
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("got %v, want ErrInvalid when no connection URL key is supplied", err)
	}
}
