package addons

import (
	"context"
	"slices"
	"testing"

	"kuso/server/internal/kube"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// seedLabeledAddon seeds a real project addon WITH the project label.
//
// NB: the shared seedAddon helper sets no labels, so a label-selector
// List never returns it. That harness gap is a large part of why the
// clone-stripping bug survived — the subscription tests exercised a
// filter whose input list was always empty.
func seedLabeledAddon(project, name, kind string) seed {
	return typedSeed(kube.GVRAddons, "KusoAddon", &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{
			Name:      project + "-" + name,
			Namespace: "kuso",
			Labels:    map[string]string{kube.LabelProject: project},
		},
		Spec: kube.KusoAddonSpec{Project: project, Kind: kind},
	})
}

// seedCloneAddon seeds an ENV-SCOPED addon clone — the shape
// previewdb.EnsureEnvAddons mints for a preview/staging env. It carries
// the project label like any other addon, PLUS the env label that marks
// it as belonging to one environment rather than the project.
func seedCloneAddon(project, name, kind, envScope string) seed {
	return typedSeed(kube.GVRAddons, "KusoAddon", &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{
			Name:      project + "-" + name,
			Namespace: "kuso",
			Labels: map[string]string{
				kube.LabelProject: project,
				kube.LabelEnv:     envScope,
			},
		},
		Spec: kube.KusoAddonSpec{Project: project, Kind: kind},
	})
}

// seedEnvWithSecrets seeds an env that already has envFromSecrets set —
// the state a live staging/preview env is actually in. The existing
// subscription tests seed envs with an empty envFromSecrets, which is
// why they never caught the clone-stripping bug.
func seedEnvWithSecrets(project, service, kind, name string, subscribed, envFrom []string) seed {
	return typedSeed(kube.GVREnvironments, "KusoEnvironment", &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "kuso",
			Labels: map[string]string{
				"kuso.sislelabs.com/project": project,
				"kuso.sislelabs.com/service": service,
				"kuso.sislelabs.com/env":     kind,
			},
		},
		Spec: kube.KusoEnvironmentSpec{
			Project:          project,
			Service:          project + "-" + service,
			Kind:             kind,
			SubscribedAddons: subscribed,
			EnvFromSecrets:   envFrom,
		},
	})
}

// TestRefresh_PreservesEnvScopedCloneConn is the regression for the
// clone-stripping data-loss bug found 2026-08-27.
//
// A staging env subscribes to base addon "db" and mounts its OWN clone
// conn, alpha-db-staging-conn. Adding ANY unrelated addon to the project
// triggers refreshEnvSecretsFiltered over every env. Before the fix the
// subscription filter built its allow-set from base names only, so:
//
//	alpha-db-staging-conn → in projectAddonSet, NOT in allow → DROPPED
//	alpha-db-conn         → in projectAddonSet, in allow     → KEPT
//
// leaving the staging pod pointed at the PRODUCTION database.
func TestRefresh_PreservesEnvScopedCloneConn(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProj("alpha"),
		seedLabeledAddon("alpha", "db", "postgres"),
		seedCloneAddon("alpha", "db-staging", "postgres", "staging"),
		seedEnvWithSecrets("alpha", "api", "staging", "alpha-api-staging",
			[]string{"db"},
			[]string{"alpha-db-staging-conn", "alpha-api-secrets"},
		),
	)

	// Add an unrelated addon — this is the trigger, nothing to do with db.
	if _, err := s.Add(context.Background(), "alpha", CreateAddonRequest{
		Name: "cache", Kind: "redis",
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	env, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-api-staging")
	if err != nil {
		t.Fatalf("get env: %v", err)
	}
	got := env.Spec.EnvFromSecrets

	if !slices.Contains(got, "alpha-db-staging-conn") {
		t.Errorf("staging env LOST its clone conn alpha-db-staging-conn; envFromSecrets=%v", got)
	}
	if slices.Contains(got, "alpha-db-conn") {
		t.Errorf("staging env gained the PRODUCTION conn alpha-db-conn — it is now pointed at "+
			"the production database; envFromSecrets=%v", got)
	}
}

// TestConnSecretsForProject_ExcludesClones: a project-wide derivation
// must never include env-scoped clones. ConnSecretsForProject seeds a
// NEW env's envFromSecrets, so a clone leaking in here would mount one
// env's private database onto a different env.
func TestConnSecretsForProject_ExcludesClones(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProj("alpha"),
		seedLabeledAddon("alpha", "db", "postgres"),
		seedCloneAddon("alpha", "db-pr-5", "postgres", "preview-pr-5"),
	)

	got, err := s.ConnSecretsForProject(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("ConnSecretsForProject: %v", err)
	}
	if slices.Contains(got, "alpha-db-pr-5-conn") {
		t.Errorf("project-wide conn list leaked preview clone alpha-db-pr-5-conn; got=%v", got)
	}
	if !slices.Contains(got, "alpha-db-conn") {
		t.Errorf("project-wide conn list dropped the real addon alpha-db-conn; got=%v", got)
	}
}

// TestConnMatchesSubscribedBase covers the clone-matching helper
// directly, including the prefix trap: subscribed "db" must match
// "db-staging" but must NOT green-light a different addon that merely
// shares a prefix, like "database".
func TestConnMatchesSubscribedBase(t *testing.T) {
	t.Parallel()
	cases := []struct {
		sec        string
		subscribed []string
		want       bool
		why        string
	}{
		{"alpha-db-staging-conn", []string{"db"}, true, "env-scoped clone of subscribed base"},
		{"alpha-db-pr-5-conn", []string{"db"}, true, "preview clone of subscribed base"},
		{"alpha-database-conn", []string{"db"}, false, "shared prefix must not match"},
		{"alpha-cache-staging-conn", []string{"db"}, false, "clone of an UNsubscribed addon"},
		{"alpha-db-conn", []string{"db"}, false, "exact base handled by the allow-set, not here"},
		{"alpha-shared", []string{"db"}, false, "not a conn secret at all"},
	}
	for _, c := range cases {
		if got := connMatchesSubscribedBase(c.sec, c.subscribed, "alpha"); got != c.want {
			t.Errorf("connMatchesSubscribedBase(%q, %v) = %v, want %v — %s",
				c.sec, c.subscribed, got, c.want, c.why)
		}
	}
}
