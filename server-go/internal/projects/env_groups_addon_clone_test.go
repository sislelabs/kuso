package projects

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

func envGroupAddonFixture(t *testing.T, spec kube.KusoAddonSpec) *Service {
	t.Helper()
	spec.Project = "acme"
	return fakeService(t,
		seedProject("acme", kube.KusoProjectSpec{BaseDomain: "apps.example.com"}),
		seedService("acme", "web", kube.KusoServiceSpec{Project: "acme", Port: 8080}),
		seedEnv("acme", "web", "production", "main", "acme-web-production"),
		typedSeed(kube.GVRAddons, "KusoAddon", "acme-db", &kube.KusoAddon{
			ObjectMeta: metav1.ObjectMeta{Name: "acme-db", Namespace: "kuso", Labels: map[string]string{labelProject: "acme"}},
			Spec:       spec,
		}),
	)
}

// An env-group copy must not inherit production's public TCP port (two
// IngressRouteTCPs on one entrypoint), HA or backups.
func TestCreateEnvGroup_CloneStripsPublicTCPHABackup(t *testing.T) {
	t.Parallel()
	s := envGroupAddonFixture(t, kube.KusoAddonSpec{
		Kind:      "postgres",
		HA:        true,
		PublicTCP: &kube.KusoAddonPublicTCP{Enabled: true, Port: 30001},
		Backup:    &kube.KusoBackup{Schedule: "0 3 * * *"},
	})
	sum, err := s.CreateEnvGroup(context.Background(), "acme", CreateEnvGroupRequest{Name: "staging"})
	if err != nil {
		t.Fatalf("CreateEnvGroup: %v", err)
	}
	if sum.Warnings == nil {
		t.Error("warnings must be a non-nil list on create")
	}
	clone, err := s.Kube.GetKusoAddon(context.Background(), "kuso", "acme-db-staging")
	if err != nil {
		t.Fatalf("get clone: %v", err)
	}
	if clone.Spec.PublicTCP != nil || clone.Spec.HA || clone.Spec.Backup != nil {
		t.Fatalf("clone kept prod-only config: publicTCP=%+v ha=%v backup=%+v", clone.Spec.PublicTCP, clone.Spec.HA, clone.Spec.Backup)
	}
}

// An external addon can't be copied; a "fresh" copy used to point at the
// prod secret with no conn mirrored (CreateContainerConfigError).
func TestCreateEnvGroup_RefusesFreshExternalAddon(t *testing.T) {
	t.Parallel()
	s := envGroupAddonFixture(t, kube.KusoAddonSpec{Kind: "postgres", External: &kube.KusoAddonExternal{SecretName: "psdb-creds"}})
	_, err := s.CreateEnvGroup(context.Background(), "acme", CreateEnvGroupRequest{Name: "staging"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	if a, _ := s.Kube.GetKusoAddon(context.Background(), "kuso", "acme-db-staging"); a != nil {
		t.Fatal("clone was created despite the refusal")
	}
	// Shared is the explicit opt-in.
	if _, err := s.CreateEnvGroup(context.Background(), "acme", CreateEnvGroupRequest{
		Name: "staging", AddonPolicy: map[string]AddonPolicy{"db": AddonShared},
	}); err != nil {
		t.Fatalf("shared external: %v", err)
	}
}

func TestCreateEnvGroup_RefusesRetainedCloneData(t *testing.T) {
	t.Parallel()
	s := envGroupAddonFixture(t, kube.KusoAddonSpec{Kind: "postgres"})
	s.Kube.Clientset = k8sfake.NewSimpleClientset(&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
		Name: "data-acme-db-staging-0", Namespace: "kuso",
		Labels: map[string]string{"app.kubernetes.io/name": "kusoaddon", "app.kubernetes.io/instance": "acme-db-staging"},
	}})
	_, err := s.CreateEnvGroup(context.Background(), "acme", CreateEnvGroupRequest{Name: "staging"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

func TestCreateEnvGroup_RejectsOverlongDerivedNames(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("acme", kube.KusoProjectSpec{BaseDomain: "apps.example.com"}),
		seedService("acme", "frontend-dashboard-x", kube.KusoServiceSpec{Project: "acme", Port: 8080}),
		seedEnv("acme", "frontend-dashboard-x", "production", "main", "acme-frontend-dashboard-x-production"),
	)
	name := "client-demo-2026xx" // acme-frontend-dashboard-x-client-demo-2026xx-production = 55
	_, err := s.CreateEnvGroup(context.Background(), "acme", CreateEnvGroupRequest{Name: name})
	if !errors.Is(err, ErrInvalid) || !errors.Is(err, kube.ErrNameTooLong) {
		t.Fatalf("err = %v, want ErrInvalid wrapping ErrNameTooLong", err)
	}
	if svc, _ := s.Kube.GetKusoService(context.Background(), "kuso", "acme-frontend-dashboard-x-"+name); svc != nil {
		t.Fatal("service clone created before the name check")
	}
}
