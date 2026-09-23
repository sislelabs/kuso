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
