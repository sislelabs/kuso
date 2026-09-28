package addons

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// A project with its own execution namespace (e2e → kuso-e2e) while the
// managed instance PG, and its kuso-instance-pg-conn Secret, live in the
// home namespace "kuso".
func customNSInstanceService(t *testing.T) *Service {
	t.Helper()
	s := fakeService(t, typedSeed(kube.GVRProjects, "KusoProject", &kube.KusoProject{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e", Namespace: "kuso"},
		Spec:       kube.KusoProjectSpec{Namespace: "kuso-e2e", DefaultRepo: &kube.KusoRepoRef{URL: "x"}},
	}))
	s.Kube.Clientset = kubefake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "kuso-instance-pg-conn", Namespace: "kuso"},
		Data:       map[string][]byte{"POOLER_HOST": []byte("kuso-instance-pg-pooler")},
	})
	s.NSResolver = kube.NewProjectNamespaceResolver(s.Kube, "kuso")
	return s
}

const instanceProjectDSN = "postgres://e2e_db:pw@kuso-instance-pg:5432/e2e_db?sslmode=disable"

func TestInstanceHasPooler_CustomNamespaceProject(t *testing.T) {
	t.Parallel()
	s := customNSInstanceService(t)
	ctx := context.Background()
	if ns := s.nsFor(ctx, "e2e"); ns != "kuso-e2e" {
		t.Fatalf("fixture: project ns = %q, want kuso-e2e", ns)
	}
	if !s.instanceHasPooler(ctx, instanceProjectDSN) {
		t.Fatal("instanceHasPooler = false for a custom-namespace project; the instance's conn Secret lives in the home namespace")
	}
}

func TestWriteInstanceConnSecret_QualifiesBareHostForCustomNamespace(t *testing.T) {
	t.Parallel()
	s := customNSInstanceService(t)
	ctx := context.Background()
	if err := s.writeInstanceAddonConnSecret(ctx, "kuso-e2e", "e2e-db", instanceProjectDSN, "pw", true); err != nil {
		t.Fatalf("write: %v", err)
	}
	sec, err := s.Kube.Clientset.CoreV1().Secrets("kuso-e2e").Get(ctx, "e2e-db-conn", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get conn secret: %v", err)
	}
	want := map[string]string{
		"POSTGRES_HOST": "kuso-instance-pg.kuso.svc",
		"DIRECT_URL":    "postgres://e2e_db:pw@kuso-instance-pg.kuso.svc:5432/e2e_db?sslmode=disable",
		"POOLER_HOST":   "kuso-instance-pg-pooler.kuso.svc",
		"DATABASE_URL":  "postgres://e2e_db:pw@kuso-instance-pg-pooler.kuso.svc:6432/e2e_db?sslmode=disable",
		"POOLER_URL":    "postgres://e2e_db:pw@kuso-instance-pg-pooler.kuso.svc:6432/e2e_db?sslmode=disable",
		"POSTGRES_PORT": "5432",
	}
	for k, v := range want {
		if got := string(sec.Data[k]); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

func TestWriteInstanceConnSecret_LeavesExternalHostsAlone(t *testing.T) {
	t.Parallel()
	for _, dsn := range []string{
		"postgres://u:pw@db.example.com:5432/app?sslmode=require",
		"postgres://u:pw@10.0.0.5:5432/app?sslmode=require",
		"postgres://u:pw@kuso-instance-pg.kuso.svc:5432/app?sslmode=disable",
	} {
		s := customNSInstanceService(t)
		ctx := context.Background()
		if err := s.writeInstanceAddonConnSecret(ctx, "kuso-e2e", "e2e-db", dsn, "pw", false); err != nil {
			t.Fatalf("write: %v", err)
		}
		sec, err := s.Kube.Clientset.CoreV1().Secrets("kuso-e2e").Get(ctx, "e2e-db-conn", metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get conn secret: %v", err)
		}
		if got := string(sec.Data["DATABASE_URL"]); got != dsn {
			t.Errorf("DATABASE_URL = %q, want untouched %q", got, dsn)
		}
		if got := string(sec.Data["DIRECT_URL"]); got != dsn {
			t.Errorf("DIRECT_URL = %q, want untouched %q", got, dsn)
		}
	}
}
