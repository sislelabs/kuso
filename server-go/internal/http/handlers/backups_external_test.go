package handlers

import (
	"context"
	"net/url"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// DATA-07: an external postgres conn may carry only DATABASE_URL. The restore
// Job must not hard-reference POSTGRES_* keys (CreateContainerConfigError
// forever) and must reach the DB through the DSN.
func TestRestoreConnEnv_ExternalUsesOptionalDSN(t *testing.T) {
	t.Parallel()
	env := restoreConnEnv("postgres", "p-psdb", true)
	seen := map[string]bool{}
	for _, e := range env {
		seen[e.Name] = true
		if ref := e.ValueFrom; ref != nil && ref.SecretKeyRef != nil {
			if ref.SecretKeyRef.Optional == nil || !*ref.SecretKeyRef.Optional {
				t.Errorf("%s is a required secret ref; an external conn may not carry %s", e.Name, ref.SecretKeyRef.Key)
			}
		}
	}
	for _, want := range []string{"EXTERNAL_RESTORE", "RESTORE_DATABASE_URL", "RESTORE_DIRECT_URL"} {
		if !seen[want] {
			t.Errorf("missing %s: %+v", want, env)
		}
	}
}

// DATA-07: a restore Job whose pod can never start must end as Failed.
func TestBuildRestoreJob_HasActiveDeadline(t *testing.T) {
	t.Parallel()
	job := buildRestoreJob("kuso", "j", "p", "db", "db", "true", nil)
	if job.Spec.ActiveDeadlineSeconds == nil || *job.Spec.ActiveDeadlineSeconds <= 0 {
		t.Fatal("restore Job has no activeDeadlineSeconds")
	}
}

// DATA-07: Download dialled external providers with sslmode=disable.
func TestAddonDSN_ExternalRequiresTLS(t *testing.T) {
	t.Parallel()
	cs := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "shop-psdb-conn", Namespace: "kuso"},
		Data:       map[string][]byte{"DATABASE_URL": []byte("postgres://u:pw@db.example.com:5432/shop")},
	})
	h := &BackupsHandler{Kube: &kube.Client{Clientset: cs}}
	cr := &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{Name: "shop-psdb"},
		Spec:       kube.KusoAddonSpec{Kind: "postgres", External: &kube.KusoAddonExternal{SecretName: "src"}},
	}
	dsn, err := h.addonDSN(context.Background(), "kuso", cr)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if u.Hostname() != "db.example.com" || u.Query().Get("sslmode") != "require" {
		t.Fatalf("external dump DSN = %s; want the provider host with sslmode=require", dsn)
	}
}
