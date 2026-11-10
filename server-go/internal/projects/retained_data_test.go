package projects

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

// AUTHZ-04: a non-purge delete keeps addon credentials (helm keep) and
// volumes; RetainedData must report them so only an instance admin can
// re-create the name and adopt them.
func TestRetainedData_ReportsKeptSecretsAndVolumes(t *testing.T) {
	t.Parallel()
	keep := map[string]string{"helm.sh/resource-policy": "keep"}
	s := fakeService(t)
	s.Kube.Clientset = kubefake.NewSimpleClientset(
		// Legacy home-namespace project: kept conn secret.
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "acme-pg-conn", Namespace: "kuso",
			Labels: map[string]string{labelProject: "acme"}, Annotations: keep}},
		// Per-project namespace project: kept volume.
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data-shop-pg-0", Namespace: "kuso-shop"}},
		// Unkept, project-labelled secret is not retained data.
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "fresh-shared", Namespace: "kuso",
			Labels: map[string]string{labelProject: "fresh"}}},
	)
	ctx := context.Background()

	for name, want := range map[string]int{"acme": 1, "shop": 1, "fresh": 0} {
		got, err := s.RetainedData(ctx, CreateProjectRequest{Name: name})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(got) != want {
			t.Errorf("%s: retained = %v, want %d item(s)", name, got, want)
		}
	}
}

// BLOB-3: backups a deleted project left in the bucket count as retained
// data; a bucket error must not block creation.
func TestRetainedData_ReportsRetainedBackups(t *testing.T) {
	t.Parallel()
	s := fakeService(t)
	s.Kube.Clientset = kubefake.NewSimpleClientset()
	s.RetainedBackups = func(_ context.Context, project string) ([]string, error) {
		if project == "acme" {
			return []string{"backups s3://b/acme/acme-pg/"}, nil
		}
		return nil, errors.New("bucket unreachable")
	}
	ctx := context.Background()
	if got, err := s.RetainedData(ctx, CreateProjectRequest{Name: "acme"}); err != nil || len(got) != 1 {
		t.Errorf("acme: retained = %v, err %v", got, err)
	}
	if got, err := s.RetainedData(ctx, CreateProjectRequest{Name: "other"}); err != nil || len(got) != 0 {
		t.Errorf("other: retained = %v, err %v", got, err)
	}
}
