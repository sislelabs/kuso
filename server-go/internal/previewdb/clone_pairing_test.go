package previewdb

import (
	"context"
	"reflect"
	"testing"

	"kuso/server/internal/kube"
)

// EnsureEnvAddonsMapped's map is the only authoritative source-conn ->
// clone-conn pairing; the preview dispatcher swaps mounts by it. A missing
// or mis-keyed entry leaves the preview mounting the PRODUCTION conn, with
// only a Warn log. These tests pin the map itself.

func TestEnsureEnvAddonsMapped_PairsEachSourceWithItsOwnClone(t *testing.T) {
	c, _ := newTestCloner(t, "alpha",
		addonCR("alpha", "pg", "postgres"),
		addonCR("alpha", "analytics", "postgres"),
	)

	conns, pairs, err := c.EnsureEnvAddonsMapped(context.Background(), "alpha", "preview-pr-5",
		EnvAddonOpts{Kinds: []string{"postgres"}, NameSuffix: "-pr-5", PreviewPR: "5"})
	if err != nil {
		t.Fatalf("EnsureEnvAddonsMapped: %v", err)
	}
	want := map[string]string{
		"alpha-pg-conn":        "alpha-pg-pr-5-conn",
		"alpha-analytics-conn": "alpha-analytics-pr-5-conn",
	}
	if !reflect.DeepEqual(pairs, want) {
		t.Fatalf("pairs = %v, want %v", pairs, want)
	}
	if len(conns) != len(want) {
		t.Fatalf("conns = %v, want one per pair", conns)
	}
}

// A clone that already exists (the re-sync path) must still be paired;
// otherwise every re-push of an open PR drops the swap.
func TestEnsureEnvAddonsMapped_PairsExistingClone(t *testing.T) {
	existing := addonCR("alpha", "pg-staging", "postgres")
	existing.Labels[kube.LabelEnv] = "staging"
	c, _ := newTestCloner(t, "alpha", addonCR("alpha", "pg", "postgres"), existing)
	c.Namespace = "kuso" // as New defaults it; the fixture leaves it empty

	_, pairs, err := c.EnsureEnvAddonsMapped(context.Background(), "alpha", "staging",
		EnvAddonOpts{Kinds: []string{"postgres"}})
	if err != nil {
		t.Fatalf("EnsureEnvAddonsMapped: %v", err)
	}
	want := map[string]string{"alpha-pg-conn": "alpha-pg-staging-conn"}
	if !reflect.DeepEqual(pairs, want) {
		t.Fatalf("pairs = %v, want %v", pairs, want)
	}
}

// The tickero case: native "db" was replaced by "psdb" while a PR was open,
// so a stale "db-pr-52" clone is still around. The map must be keyed by the
// source that exists now, not reconstructed from the old clone's name, and
// external sources (never cloned) must not appear at all.
func TestEnsureEnvAddonsMapped_KeysByCurrentSourceNotCloneName(t *testing.T) {
	stale := addonCR("alpha", "db-pr-52", "postgres")
	stale.Labels[kube.LabelEnv] = "preview-pr-52"
	external := addonCR("alpha", "ext", "postgres")
	external.Spec.External = &kube.KusoAddonExternal{SecretName: "alpha-ext-external"}
	c, _ := newTestCloner(t, "alpha", addonCR("alpha", "psdb", "postgres"), stale, external)

	_, pairs, err := c.EnsureEnvAddonsMapped(context.Background(), "alpha", "preview-pr-52",
		EnvAddonOpts{Kinds: []string{"postgres"}, NameSuffix: "-pr-52", PreviewPR: "52"})
	if err != nil {
		t.Fatalf("EnsureEnvAddonsMapped: %v", err)
	}
	want := map[string]string{"alpha-psdb-conn": "alpha-psdb-pr-52-conn"}
	if !reflect.DeepEqual(pairs, want) {
		t.Fatalf("pairs = %v, want %v", pairs, want)
	}
}

// F8c: a preview's redis is per-PR too. Sharing production redis let PR
// code read prod sessions/queues and write into them (queue jobs, cache
// poisoning). Redis clones start empty; no seed is attempted.
func TestEnsurePRAddons_ClonesRedis(t *testing.T) {
	c, _ := newTestCloner(t, "alpha", addonCR("alpha", "cache", "redis"))
	c.Namespace = "kuso"

	_, pairs, err := c.EnsurePRAddons(context.Background(), "alpha", 9)
	if err != nil {
		t.Fatalf("EnsurePRAddons: %v", err)
	}
	want := map[string]string{"alpha-cache-conn": "alpha-cache-pr-9-conn"}
	if !reflect.DeepEqual(pairs, want) {
		t.Fatalf("pairs = %v, want %v", pairs, want)
	}
	clone, err := c.Kube.GetKusoAddon(context.Background(), "kuso", "alpha-cache-pr-9")
	if err != nil {
		t.Fatalf("redis clone CR: %v", err)
	}
	if clone.Labels[previewPRLabel] != "9" {
		t.Errorf("redis clone lacks preview-pr label (DeletePRAddons would leak it): %v", clone.Labels)
	}
	if _, pending := clone.Annotations[seedPendingAnnotation]; pending {
		t.Errorf("redis clone marked seed-pending; only postgres is seeded")
	}
}

// Projects without redis (or with only non-cloned kinds) still preview.
func TestEnsurePRAddons_NoCloneableAddons(t *testing.T) {
	c, _ := newTestCloner(t, "alpha", addonCR("alpha", "files", "s3"))
	c.Namespace = "kuso"

	conns, pairs, err := c.EnsurePRAddons(context.Background(), "alpha", 9)
	if err != nil {
		t.Fatalf("EnsurePRAddons: %v", err)
	}
	if len(conns) != 0 || len(pairs) != 0 {
		t.Fatalf("s3-only project cloned something: conns=%v pairs=%v", conns, pairs)
	}
}
