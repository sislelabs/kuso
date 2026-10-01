package previewdb

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

func existingPRClone() *kube.KusoAddon {
	a := addonCR("alpha", "pg-pr-7", "postgres")
	a.Labels[kube.LabelEnv] = "preview-pr-7"
	a.Labels[previewPRLabel] = "7"
	return a
}

var prOpts = EnvAddonOpts{Kinds: []string{"postgres"}, SeedAll: true, NameSuffix: "-pr-7", PreviewPR: "7"}

// A PR push re-runs EnsurePRAddons against the clone that already exists.
// It must not re-seed it: that pg_dump --clean'd production over whatever
// the reviewers had in the preview, once per push.
func TestEnsureEnvAddons_ExistingCloneIsNotReseeded(t *testing.T) {
	c, dyn := newTestCloner(t, "alpha", addonCR("alpha", "pg", "postgres"), existingPRClone())
	c.Namespace = "kuso"
	c.Kube.Clientset = kubefake.NewSimpleClientset()
	c.BaseCtx = cancelledCtx()

	if _, err := c.EnsureEnvAddons(context.Background(), "alpha", "preview-pr-7", prOpts); err != nil {
		t.Fatalf("EnsureEnvAddons: %v", err)
	}
	if got := getAddon(t, dyn, "alpha-pg-pr-7").Annotations[seedPendingAnnotation]; got != "" {
		t.Fatalf("existing clone was re-seeded (seed-pending=%q)", got)
	}

	resync := prOpts
	resync.Resync = true
	if _, err := c.EnsureEnvAddons(context.Background(), "alpha", "preview-pr-7", resync); err != nil {
		t.Fatalf("EnsureEnvAddons resync: %v", err)
	}
	if got := getAddon(t, dyn, "alpha-pg-pr-7").Annotations[seedPendingAnnotation]; got != "alpha-pg" {
		t.Fatalf("explicit resync: seed-pending=%q, want alpha-pg", got)
	}
}

// --seed-from staging must dump staging's clone, not production.
func TestEnsureEnvAddons_SeedFromScopeUsesThatEnvsClone(t *testing.T) {
	staging := addonCR("alpha", "pg-staging", "postgres")
	staging.Labels[kube.LabelEnv] = "staging"
	staging.Annotations = map[string]string{envGroupSourceAddonAnnotation: "alpha-pg"}
	c, dyn := newTestCloner(t, "alpha", addonCR("alpha", "pg", "postgres"), staging)
	c.Namespace = "kuso"
	c.Kube.Clientset = kubefake.NewSimpleClientset()
	c.BaseCtx = cancelledCtx()

	if _, err := c.EnsureEnvAddons(context.Background(), "alpha", "qa", EnvAddonOpts{
		Kinds: []string{"postgres"}, SeedAll: true, SeedFromScope: "staging",
	}); err != nil {
		t.Fatalf("EnsureEnvAddons: %v", err)
	}
	if got := getAddon(t, dyn, "alpha-pg-qa").Annotations[seedPendingAnnotation]; got != "alpha-pg-staging" {
		t.Fatalf("seed source = %q, want alpha-pg-staging", got)
	}
}

// With no re-seed, the push's new image still has to be migrated against the
// kept clone (the build poller skips release Jobs for previews).
func TestWatchAndMigrate_MigratesOnlyOnNewImage(t *testing.T) {
	env := previewEnvCR("alpha-web-pr-7", []string{"alpha-pg-pr-7-conn"}, []string{"migrate"}, "new")
	env.Labels = map[string]string{kube.LabelEnv: "preview-pr-7"}
	c := newFakeCloner(t, &env)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	c.watchAndMigrate(ctx, "kuso", "alpha", "preview-pr-7", "alpha-pg-pr-7", map[string]string{"alpha-web-pr-7": "new"}, 10*time.Millisecond)
	jobs, _ := c.Kube.Clientset.BatchV1().Jobs("kuso").List(context.Background(), metav1.ListOptions{})
	if len(jobs.Items) != 0 {
		t.Fatalf("unchanged image migrated: %d jobs", len(jobs.Items))
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel2()
	c.watchAndMigrate(ctx2, "kuso", "alpha", "preview-pr-7", "alpha-pg-pr-7", map[string]string{"alpha-web-pr-7": "old"}, 10*time.Millisecond)
	jobs, _ = c.Kube.Clientset.BatchV1().Jobs("kuso").List(context.Background(), metav1.ListOptions{})
	if len(jobs.Items) != 1 {
		t.Fatalf("new image: %d migrate jobs, want 1", len(jobs.Items))
	}
}
