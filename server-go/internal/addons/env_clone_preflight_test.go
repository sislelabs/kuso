package addons

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"github.com/lib/pq"
)

// DATA-10: an env-group clone written straight to the CR skipped Add's
// retained-data check, so a same-name leftover PVC was silently mounted.
func TestPreflightEnvClone_RefusesRetainedData(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProj("alpha"))
	s.Kube.Clientset = kubefake.NewSimpleClientset(addonPVC("data-alpha-db-staging-0", "alpha-db-staging"))
	if err := s.PreflightEnvClone(context.Background(), "alpha", "db-staging", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("got %v, want ErrConflict", err)
	}
	s.Kube.Clientset = kubefake.NewSimpleClientset()
	if err := s.PreflightEnvClone(context.Background(), "alpha", "db-staging", ""); err != nil {
		t.Fatalf("clean name refused: %v", err)
	}
}

// DATA-10, instance-shared: a surviving conn Secret or database would be
// re-adopted with the previous group's data.
func TestPreflightEnvClone_InstanceRefusesSurvivingConnOrDB(t *testing.T) {
	p := uniqueProject(t)
	s, adminDSN := instancePGService(t, p)
	ns := "kuso-" + p
	ctx := context.Background()

	if err := s.PreflightEnvClone(ctx, p, "db-staging", "pg"); err != nil {
		t.Fatalf("clean instance clone refused: %v", err)
	}
	conn := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: p + "-db-staging-conn", Namespace: ns}}
	if _, err := s.Kube.Clientset.CoreV1().Secrets(ns).Create(ctx, conn, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := s.PreflightEnvClone(ctx, p, "db-staging", "pg"); !errors.Is(err, ErrConflict) {
		t.Fatalf("surviving conn secret: got %v, want ErrConflict", err)
	}
	_ = s.Kube.Clientset.CoreV1().Secrets(ns).Delete(ctx, conn.Name, metav1.DeleteOptions{})

	ident := pgIdentifier(p, "db-staging")
	dropTestIdent(t, adminDSN, ident)
	adminExec(t, adminDSN, `CREATE DATABASE `+pq.QuoteIdentifier(ident))
	if err := s.PreflightEnvClone(ctx, p, "db-staging", "pg"); !errors.Is(err, ErrConflict) {
		t.Fatalf("surviving database: got %v, want ErrConflict", err)
	}
}
