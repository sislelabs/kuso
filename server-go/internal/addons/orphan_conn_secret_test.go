package addons

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"kuso/server/internal/kube"
)

// A clone addon's <addon>-conn Secret must not outlive the database it
// describes.
//
// The chart annotates it resource-policy: keep so it survives helm
// uninstall — correct while a retained PVC or database still holds data
// initialised with that password. Once the clone's database is dropped,
// the Secret is a credential for nothing. Worse, it is what made the
// leak invisible: all 16 orphaned databases still had a live-looking
// *-conn Secret, so "is anything referencing this?" answered yes.
func TestDelete_CloneRemovesConnSecret(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := fakeServiceWithSecrets(t, seedProj("bukvite"))

	// An env-group clone: instance-pg backed, labelled env=staging.
	cr := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "application.kuso.sislelabs.com/v1alpha1",
		"kind":       "KusoAddon",
		"metadata": map[string]any{
			"name":      "bukvite-db-staging",
			"namespace": "kuso",
			"labels": map[string]any{
				"kuso.sislelabs.com/project": "bukvite",
				kube.LabelEnv:                "staging",
			},
		},
		"spec": map[string]any{
			"kind":             "postgres",
			"useInstanceAddon": "pg",
		},
	}}
	gvr := schema.GroupVersionResource{
		Group: "application.kuso.sislelabs.com", Version: "v1alpha1", Resource: "kusoaddons",
	}
	if _, err := s.Kube.Dynamic.Resource(gvr).Namespace("kuso").
		Create(ctx, cr, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed addon CR: %v", err)
	}
	if _, err := s.Kube.Clientset.CoreV1().Secrets("kuso").Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "bukvite-db-staging-conn", Namespace: "kuso"},
		Data:       map[string][]byte{"POSTGRES_DB": []byte("bukvite_db_staging")},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed conn secret: %v", err)
	}

	if err := s.Delete(ctx, "bukvite", "db-staging"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := s.Kube.Clientset.CoreV1().Secrets("kuso").
		Get(ctx, "bukvite-db-staging-conn", metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("clone conn secret survived the delete (err=%v) — it is a "+
			"credential for a database that no longer exists, and it makes "+
			"the orphan look referenced", err)
	}
}

// The mirror case, and the one that must never regress: a project's OWN
// addon retains both its data and its conn Secret, so a delete+re-add
// reuses the password the surviving database was initialised with.
func TestDelete_ProjectOwnAddonKeepsConnSecret(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := fakeServiceWithSecrets(t, seedProj("bukvite"))

	cr := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "application.kuso.sislelabs.com/v1alpha1",
		"kind":       "KusoAddon",
		"metadata": map[string]any{
			"name":      "bukvite-db",
			"namespace": "kuso",
			"labels":    map[string]any{"kuso.sislelabs.com/project": "bukvite"},
		},
		"spec": map[string]any{
			"kind":             "postgres",
			"useInstanceAddon": "pg",
		},
	}}
	gvr := schema.GroupVersionResource{
		Group: "application.kuso.sislelabs.com", Version: "v1alpha1", Resource: "kusoaddons",
	}
	if _, err := s.Kube.Dynamic.Resource(gvr).Namespace("kuso").
		Create(ctx, cr, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed addon CR: %v", err)
	}
	if _, err := s.Kube.Clientset.CoreV1().Secrets("kuso").Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "bukvite-db-conn", Namespace: "kuso"},
		Data:       map[string][]byte{"POSTGRES_DB": []byte("bukvite_db")},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed conn secret: %v", err)
	}

	if err := s.Delete(ctx, "bukvite", "db"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := s.Kube.Clientset.CoreV1().Secrets("kuso").
		Get(ctx, "bukvite-db-conn", metav1.GetOptions{}); err != nil {
		t.Fatalf("production conn secret was removed (%v) — a delete+re-add "+
			"would mint a new password the surviving database rejects", err)
	}
}
