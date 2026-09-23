package projects

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// Env-scoped addon clones belong to an env SCOPE (staging, preview-pr-N),
// not to one service: every service's env in that scope mounts the same
// <project>-db-staging. Deleting one service's env must leave them alone
// while any sibling env in the scope is still live.

func seedScopedAddon(project, name string, labels map[string]string, instance string) seed {
	l := map[string]string{labelProject: project}
	for k, v := range labels {
		l[k] = v
	}
	return typedSeed(kube.GVRAddons, "KusoAddon", name, &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso", Labels: l},
		Spec:       kube.KusoAddonSpec{Project: project, Kind: "postgres", UseInstanceAddon: instance},
	})
}

func addonExists(t *testing.T, s *Service, name string) bool {
	t.Helper()
	_, err := s.Kube.Dynamic.Resource(kube.GVRAddons).Namespace("kuso").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("get addon %s: %v", name, err)
	}
	return err == nil
}

func TestDeleteEnvironment_KeepsStagingAddonsWhileSiblingEnvLives(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedEnv("alpha", "api", "staging", "main", "alpha-api-staging"),
		seedEnv("alpha", "worker", "staging", "main", "alpha-worker-staging"),
		seedScopedAddon("alpha", "alpha-db-staging", map[string]string{labelEnv: "staging"}, ""),
	)
	ctx := context.Background()

	if err := s.DeleteEnvironment(ctx, "alpha", "alpha-worker-staging"); err != nil {
		t.Fatalf("DeleteEnvironment(worker-staging): %v", err)
	}
	if !addonExists(t, s, "alpha-db-staging") {
		t.Fatal("alpha-db-staging deleted while alpha-api-staging still uses it")
	}

	if err := s.DeleteEnvironment(ctx, "alpha", "alpha-api-staging"); err != nil {
		t.Fatalf("DeleteEnvironment(api-staging): %v", err)
	}
	if addonExists(t, s, "alpha-db-staging") {
		t.Fatal("alpha-db-staging must be reclaimed once the last staging env is gone")
	}
}

func TestDeleteEnvironment_KeepsPreviewAddonsWhileSiblingPreviewLives(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedEnv("alpha", "api", "preview-pr-7", "feat", "alpha-api-pr-7"),
		seedEnv("alpha", "worker", "preview-pr-7", "feat", "alpha-worker-pr-7"),
		seedScopedAddon("alpha", "alpha-db-pr-7", map[string]string{
			labelEnv: "preview-pr-7", "kuso.sislelabs.com/preview-pr": "7",
		}, ""),
	)
	ctx := context.Background()

	if err := s.DeleteEnvironment(ctx, "alpha", "alpha-worker-pr-7"); err != nil {
		t.Fatalf("DeleteEnvironment(worker-pr-7): %v", err)
	}
	if !addonExists(t, s, "alpha-db-pr-7") {
		t.Fatal("alpha-db-pr-7 deleted while alpha-api-pr-7 still uses it")
	}

	if err := s.DeleteEnvironment(ctx, "alpha", "alpha-api-pr-7"); err != nil {
		t.Fatalf("DeleteEnvironment(api-pr-7): %v", err)
	}
	if addonExists(t, s, "alpha-db-pr-7") {
		t.Fatal("alpha-db-pr-7 must be reclaimed once the last PR-7 env is gone")
	}
}

func TestDeleteService_KeepsSiblingServicesStagingAddons(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "api", kube.KusoServiceSpec{Project: "alpha"}),
		seedService("alpha", "worker", kube.KusoServiceSpec{Project: "alpha"}),
		seedEnv("alpha", "api", "staging", "main", "alpha-api-staging"),
		seedEnv("alpha", "worker", "staging", "main", "alpha-worker-staging"),
		seedScopedAddon("alpha", "alpha-db-staging", map[string]string{labelEnv: "staging"}, ""),
	)
	if err := s.DeleteService(context.Background(), "alpha", "worker"); err != nil {
		t.Fatalf("DeleteService(worker): %v", err)
	}
	if !addonExists(t, s, "alpha-db-staging") {
		t.Fatal("service delete took out the staging DB alpha-api-staging still uses")
	}
}

// The last service's production env going away must still never delete an
// addon labelled env=production: that is the project's own data.
func TestDeleteService_NeverDeletesProductionScopedAddon(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "worker", kube.KusoServiceSpec{Project: "alpha"}),
		seedEnv("alpha", "worker", "production", "main", "alpha-worker-production"),
		seedScopedAddon("alpha", "alpha-db", map[string]string{labelEnv: "production"}, ""),
	)
	if err := s.DeleteService(context.Background(), "alpha", "worker"); err != nil {
		t.Fatalf("DeleteService(worker): %v", err)
	}
	if !addonExists(t, s, "alpha-db") {
		t.Fatal("service delete deleted the env=production addon alpha-db")
	}
}

func TestDeleteEnvironment_LastStagingEnvCleansInstanceClone(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedEnv("alpha", "api", "staging", "main", "alpha-api-staging"),
		seedEnv("alpha", "worker", "staging", "main", "alpha-worker-staging"),
		seedScopedAddon("alpha", "alpha-db-staging", map[string]string{labelEnv: "staging"}, "cluster-pg"),
	)
	var cleaned []string
	s.CleanupInstanceAddon = func(_ context.Context, project, short string) error {
		if !addonExists(t, s, "alpha-db-staging") {
			t.Error("instance cleanup ran after the CR was deleted; it needs spec.useInstanceAddon")
		}
		cleaned = append(cleaned, project+"/"+short)
		return nil
	}
	ctx := context.Background()

	if err := s.DeleteEnvironment(ctx, "alpha", "alpha-worker-staging"); err != nil {
		t.Fatalf("DeleteEnvironment(worker-staging): %v", err)
	}
	if len(cleaned) != 0 {
		t.Fatalf("instance DB dropped while alpha-api-staging still uses it: %v", cleaned)
	}
	if err := s.DeleteEnvironment(ctx, "alpha", "alpha-api-staging"); err != nil {
		t.Fatalf("DeleteEnvironment(api-staging): %v", err)
	}
	if len(cleaned) != 1 || cleaned[0] != "alpha/db-staging" {
		t.Fatalf("instance cleanup calls = %v, want [alpha/db-staging]", cleaned)
	}
}

// Project delete keeps the project's own instance DB (retain semantics) but
// must drop an env clone's, which nothing will ever reattach.
func TestProjectDelete_CleansInstanceClonesOnly(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedScopedAddon("alpha", "alpha-db", nil, "cluster-pg"),
		seedScopedAddon("alpha", "alpha-db-prod", map[string]string{labelEnv: "production"}, "cluster-pg"),
		seedScopedAddon("alpha", "alpha-db-staging", map[string]string{labelEnv: "staging"}, "cluster-pg"),
		seedScopedAddon("alpha", "alpha-db-pr-3", map[string]string{
			labelEnv: "preview-pr-3", "kuso.sislelabs.com/preview-pr": "3",
		}, "cluster-pg"),
	)
	var cleaned []string
	s.CleanupInstanceAddon = func(_ context.Context, project, short string) error {
		cleaned = append(cleaned, project+"/"+short)
		return nil
	}
	if err := s.Delete(context.Background(), "alpha"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	want := map[string]bool{"alpha/db-staging": true, "alpha/db-pr-3": true}
	if len(cleaned) != len(want) {
		t.Fatalf("instance cleanup calls = %v, want db-staging + db-pr-3 only", cleaned)
	}
	for _, c := range cleaned {
		if !want[c] {
			t.Fatalf("instance cleanup called for %s; a project's own DB must be kept", c)
		}
	}
}
