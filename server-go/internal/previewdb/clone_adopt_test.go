package previewdb

import (
	"context"
	"testing"

	"kuso/server/internal/kube"
)

// DATA-09: a real project addon that happens to sit at the clone's name
// (db-staging, events-pr-2) must not be adopted as the env's clone — the
// env would mount it and migrations would run against it.
func TestEnsureEnvAddons_RefusesToAdoptNonCloneAtCloneName(t *testing.T) {
	c, _ := newTestCloner(t, "alpha",
		addonCR("alpha", "db", "postgres"),
		addonCR("alpha", "db-staging", "postgres"), // a real project addon, no env label
	)
	c.Namespace = "kuso"
	if _, err := c.EnsureEnvAddons(context.Background(), "alpha", "staging", EnvAddonOpts{Kinds: []string{"postgres"}}); err == nil {
		t.Fatal("adopted a project addon as staging's clone; want an error")
	}
}

// A genuine clone (env label = scope) is still reused.
func TestEnsureEnvAddons_ReusesLabelledClone(t *testing.T) {
	clone := addonCR("alpha", "db-staging", "postgres")
	clone.Labels[kube.LabelEnv] = "staging"
	c, _ := newTestCloner(t, "alpha", addonCR("alpha", "db", "postgres"), clone)
	c.Namespace = "kuso"
	conns, err := c.EnsureEnvAddons(context.Background(), "alpha", "staging", EnvAddonOpts{Kinds: []string{"postgres"}})
	if err != nil {
		t.Fatalf("EnsureEnvAddons: %v", err)
	}
	if len(conns) != 1 || conns[0] != "alpha-db-staging-conn" {
		t.Fatalf("conns = %v", conns)
	}
}
