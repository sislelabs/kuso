package previewdb

import (
	"context"
	"testing"

	"kuso/server/internal/kube"
)

// An external addon is a pointer at someone else's database — there is no
// StatefulSet to copy and kuso cannot create a database on a managed provider.
// Cloning one produces an addon whose conn secret is a duplicate of production
// credentials, so a PR branch would read and write live data.
//
// Previews keep using a kuso-managed Postgres instead. That means the external
// source is skipped here, and a project that wants preview databases keeps a
// native postgres addon alongside its external one.
func TestEnsureEnvAddons_SkipsExternalSources(t *testing.T) {
	external := addonCR("alpha", "psdb", "postgres")
	external.Spec.External = &kube.KusoAddonExternal{SecretName: "alpha-psdb-external"}

	c, dyn := newTestCloner(t, "alpha",
		addonCR("alpha", "pg", "postgres"), // native source — should clone
		external,                           // external source — must not
	)

	conns, err := c.EnsureEnvAddons(context.Background(), "alpha", "preview-pr-7",
		EnvAddonOpts{Kinds: []string{"postgres"}, NameSuffix: "-pr-7"})
	if err != nil {
		t.Fatalf("EnsureEnvAddons: %v", err)
	}

	if len(conns) != 1 || conns[0] != "alpha-pg-pr-7-conn" {
		t.Fatalf("conns = %v, want only the native clone [alpha-pg-pr-7-conn]", conns)
	}
	if getAddon(t, dyn, "alpha-psdb-pr-7") != nil {
		t.Error("cloned an external addon — the clone would carry production " +
			"credentials, letting a PR env write to the live database")
	}
}

// A project whose ONLY postgres is external gets no preview database rather
// than a clone pointed at production.
func TestEnsureEnvAddons_ExternalOnlyProjectClonesNothing(t *testing.T) {
	external := addonCR("alpha", "psdb", "postgres")
	external.Spec.External = &kube.KusoAddonExternal{SecretName: "alpha-psdb-external"}

	c, dyn := newTestCloner(t, "alpha", external)

	conns, err := c.EnsureEnvAddons(context.Background(), "alpha", "preview-pr-9",
		EnvAddonOpts{Kinds: []string{"postgres"}, NameSuffix: "-pr-9"})
	if err != nil {
		t.Fatalf("EnsureEnvAddons: %v", err)
	}
	if len(conns) != 0 {
		t.Fatalf("conns = %v, want none", conns)
	}
	if getAddon(t, dyn, "alpha-psdb-pr-9") != nil {
		t.Error("minted a clone of the external addon")
	}
}
