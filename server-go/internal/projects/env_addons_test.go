package projects

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// seedAddon seeds a project addon CR (so statefulAddonKinds / postgresConnForEnv
// can see it). Mirrors the addons-package helper but in this package's harness.
func seedAddon(project, short, kind string) seed {
	return typedSeed(kube.GVRAddons, "KusoAddon", project+"-"+short, &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{
			Name:      project + "-" + short,
			Namespace: "kuso",
			Labels:    map[string]string{labelProject: project},
		},
		Spec: kube.KusoAddonSpec{Project: project, Kind: kind},
	})
}

// fakeEnvAddons returns an EnvAddons func that records its call and returns the
// given clone conn-secrets, so AddEnvironment's swap logic can be asserted. The
// origin map is derived from the conventional "<base>-<scope>-conn" names —
// fine for fixtures; production gets it from EnsureEnvAddonsMapped.
func fakeEnvAddons(clones []string, got *struct {
	scope   string
	kinds   []string
	seedAll bool
	called  bool
}) func(ctx context.Context, project, envScope string, kinds []string, seedAll bool) ([]string, map[string]string, error) {
	return func(ctx context.Context, project, envScope string, kinds []string, seedAll bool) ([]string, map[string]string, error) {
		got.scope = envScope
		got.kinds = kinds
		got.seedAll = seedAll
		got.called = true
		byOrigin := make(map[string]string, len(clones))
		for _, c := range clones {
			byOrigin[strings.TrimSuffix(c, "-"+envScope+"-conn")+"-conn"] = c
		}
		return clones, byOrigin, nil
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// TestAddEnvironment_SwapsToPerEnvAddons: a new named env drops the project addon
// conn-secrets from EnvFromSecrets and appends the per-env clone conn-secrets.
func TestAddEnvironment_SwapsToPerEnvAddons(t *testing.T) {
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Runtime: "dockerfile", Port: 3000}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
		seedAddon("alpha", "pg", "postgres"),
	)
	// The project's shared addon conn-secrets (what a shared env would mount).
	s.AddonConnSecrets = func(ctx context.Context, project string) ([]string, error) {
		return []string{"alpha-pg-conn", "alpha-shared"}, nil
	}
	var got struct {
		scope   string
		kinds   []string
		seedAll bool
		called  bool
	}
	s.EnvAddons = fakeEnvAddons([]string{"alpha-pg-staging-conn"}, &got)

	env, err := s.AddEnvironment(context.Background(), "alpha", "web", CreateEnvRequest{Name: "staging", Branch: "staging"})
	if err != nil {
		t.Fatalf("AddEnvironment: %v", err)
	}

	if !got.called {
		t.Fatalf("EnvAddons was not called for a default (non-share-addons) env")
	}
	if got.scope != "staging" {
		t.Fatalf("EnvAddons scope = %q, want staging", got.scope)
	}
	// Default kinds = the project's stateful kinds (postgres here).
	if !contains(got.kinds, "postgres") {
		t.Fatalf("EnvAddons kinds = %v, want to include postgres", got.kinds)
	}
	if got.seedAll {
		t.Fatalf("seedAll should be false without --seed-from")
	}

	efs := env.Spec.EnvFromSecrets
	if contains(efs, "alpha-pg-conn") {
		t.Fatalf("project addon conn alpha-pg-conn should have been dropped: %v", efs)
	}
	if !contains(efs, "alpha-pg-staging-conn") {
		t.Fatalf("per-env clone conn alpha-pg-staging-conn missing: %v", efs)
	}
}

// TestAddEnvironment_RescopesExplicitAddonSecretRef: a new named env must
// rewrite an explicit DATABASE_URL secretKeyRef (set on the service against the
// production addon, e.g. alpha-pg-conn) onto its OWN clone conn
// (alpha-pg-staging-conn). Without this, the explicit env entry wins over
// envFromSecrets on key collision and staging's DATABASE_URL silently resolves
// to the production database — defeating per-env isolation.
func TestAddEnvironment_RescopesExplicitAddonSecretRef(t *testing.T) {
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{
			Runtime: "dockerfile",
			Port:    3000,
			// Mirrors what SetEnv writes for `DATABASE_URL=${{ pg.DATABASE_URL }}`:
			// an explicit secretKeyRef against the PRODUCTION addon conn.
			EnvVars: []kube.KusoEnvVar{
				{Name: "SMOKE_TAG", Value: "baseline"},
				{Name: "DATABASE_URL", ValueFrom: map[string]any{
					"secretKeyRef": map[string]any{
						"name": "alpha-pg-conn",
						"key":  "DATABASE_URL",
					},
				}},
			},
		}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
		seedAddon("alpha", "pg", "postgres"),
	)
	s.AddonConnSecrets = func(ctx context.Context, project string) ([]string, error) {
		return []string{"alpha-pg-conn", "alpha-shared"}, nil
	}
	var got struct {
		scope   string
		kinds   []string
		seedAll bool
		called  bool
	}
	s.EnvAddons = fakeEnvAddons([]string{"alpha-pg-staging-conn"}, &got)

	env, err := s.AddEnvironment(context.Background(), "alpha", "web", CreateEnvRequest{Name: "staging", Branch: "staging"})
	if err != nil {
		t.Fatalf("AddEnvironment: %v", err)
	}

	var dbURL *kube.KusoEnvVar
	for i := range env.Spec.EnvVars {
		if env.Spec.EnvVars[i].Name == "DATABASE_URL" {
			dbURL = &env.Spec.EnvVars[i]
		}
	}
	if dbURL == nil {
		t.Fatalf("DATABASE_URL missing from staging env vars: %+v", env.Spec.EnvVars)
	}
	skr, ok := dbURL.ValueFrom["secretKeyRef"].(map[string]any)
	if !ok {
		t.Fatalf("DATABASE_URL lost its secretKeyRef: %+v", dbURL.ValueFrom)
	}
	if name, _ := skr["name"].(string); name != "alpha-pg-staging-conn" {
		t.Fatalf("DATABASE_URL secretKeyRef.name = %q, want alpha-pg-staging-conn (staging must not point at the production conn)", name)
	}
	if key, _ := skr["key"].(string); key != "DATABASE_URL" {
		t.Fatalf("DATABASE_URL secretKeyRef.key = %q, want DATABASE_URL (key must be preserved)", key)
	}
	// The production env's source spec must be untouched (no aliasing mutation).
	srcSvc, _ := s.GetService(context.Background(), "alpha", "web")
	for _, e := range srcSvc.Spec.EnvVars {
		if e.Name != "DATABASE_URL" {
			continue
		}
		srcSKR, _ := e.ValueFrom["secretKeyRef"].(map[string]any)
		if name, _ := srcSKR["name"].(string); name != "alpha-pg-conn" {
			t.Fatalf("source service spec was mutated: DATABASE_URL now points at %q, want alpha-pg-conn", name)
		}
	}
}

// TestRescopeAddonConnRefs covers the helper that both AddEnvironment and
// propagateChangedToEnvs rely on to keep a non-production env's explicit addon
// secretKeyRef pointed at its OWN clone conn.
func TestRescopeAddonConnRefs(t *testing.T) {
	mkRef := func(name string) kube.KusoEnvVar {
		return kube.KusoEnvVar{Name: "DATABASE_URL", ValueFrom: map[string]any{
			"secretKeyRef": map[string]any{"name": name, "key": "DATABASE_URL"},
		}}
	}
	skrName := func(e kube.KusoEnvVar) string {
		skr, _ := e.ValueFrom["secretKeyRef"].(map[string]any)
		n, _ := skr["name"].(string)
		return n
	}

	t.Run("rewrites base conn to clone when clone present", func(t *testing.T) {
		in := []kube.KusoEnvVar{{Name: "PLAIN", Value: "x"}, mkRef("alpha-db-conn")}
		out := rescopeAddonConnRefs(in, []string{"alpha-db-conn"}, []string{"alpha-db-staging-conn"}, "staging")
		if got := skrName(out[1]); got != "alpha-db-staging-conn" {
			t.Fatalf("got %q, want alpha-db-staging-conn", got)
		}
		if out[0].Value != "x" {
			t.Fatalf("plain var mutated: %+v", out[0])
		}
	})

	t.Run("no clone present leaves ref untouched (no dangling ref)", func(t *testing.T) {
		in := []kube.KusoEnvVar{mkRef("alpha-db-conn")}
		// clone not provisioned for this scope → must NOT rewrite to a
		// non-existent secret.
		out := rescopeAddonConnRefs(in, []string{"alpha-db-conn"}, []string{"alpha-redis-staging-conn"}, "staging")
		if got := skrName(out[0]); got != "alpha-db-conn" {
			t.Fatalf("got %q, want alpha-db-conn (unchanged)", got)
		}
	})

	t.Run("production scope is a no-op", func(t *testing.T) {
		in := []kube.KusoEnvVar{mkRef("alpha-db-conn")}
		out := rescopeAddonConnRefs(in, []string{"alpha-db-conn"}, []string{"alpha-db-production-conn"}, "production")
		if got := skrName(out[0]); got != "alpha-db-conn" {
			t.Fatalf("production should not rescope; got %q", got)
		}
	})

	t.Run("non-addon refs (instance-shared) untouched", func(t *testing.T) {
		in := []kube.KusoEnvVar{mkRef("kuso-instance-shared")}
		out := rescopeAddonConnRefs(in, []string{"alpha-db-conn"}, []string{"alpha-db-staging-conn"}, "staging")
		if got := skrName(out[0]); got != "kuso-instance-shared" {
			t.Fatalf("non-base-conn ref mutated; got %q", got)
		}
	})

	t.Run("does not mutate input slice's nested maps", func(t *testing.T) {
		src := mkRef("alpha-db-conn")
		in := []kube.KusoEnvVar{src}
		_ = rescopeAddonConnRefs(in, []string{"alpha-db-conn"}, []string{"alpha-db-staging-conn"}, "staging")
		// the original src's nested map must still say alpha-db-conn
		if got := skrName(src); got != "alpha-db-conn" {
			t.Fatalf("input nested map was mutated: got %q", got)
		}
	})
}

// TestAddEnvironment_ShareAddonsKeepsProjectConns: --share-addons keeps the
// shared project addon conn-secrets and does NOT provision per-env addons.
func TestAddEnvironment_ShareAddonsKeepsProjectConns(t *testing.T) {
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Runtime: "dockerfile", Port: 3000}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
		seedAddon("alpha", "pg", "postgres"),
	)
	s.AddonConnSecrets = func(ctx context.Context, project string) ([]string, error) {
		return []string{"alpha-pg-conn"}, nil
	}
	var got struct {
		scope   string
		kinds   []string
		seedAll bool
		called  bool
	}
	s.EnvAddons = fakeEnvAddons([]string{"alpha-pg-staging-conn"}, &got)

	env, err := s.AddEnvironment(context.Background(), "alpha", "web", CreateEnvRequest{Name: "staging", Branch: "staging", ShareAddons: true})
	if err != nil {
		t.Fatalf("AddEnvironment: %v", err)
	}
	if got.called {
		t.Fatalf("EnvAddons should NOT be called with --share-addons")
	}
	if !contains(env.Spec.EnvFromSecrets, "alpha-pg-conn") {
		t.Fatalf("shared project addon conn should be kept: %v", env.Spec.EnvFromSecrets)
	}
	if contains(env.Spec.EnvFromSecrets, "alpha-pg-staging-conn") {
		t.Fatalf("no per-env clone should be mounted with --share-addons: %v", env.Spec.EnvFromSecrets)
	}
}

// TestAddEnvironment_ClonesRespectSubscription: a new env gets its own copy
// of every stateful addon, but only mounts the clones of addons the service
// subscribes to. The e2e frontend with subscribedAddons=[] was handed its
// staging DATABASE_URL and REDIS_URL because clones were appended after the
// subscription filter had already run.
func TestAddEnvironment_ClonesRespectSubscription(t *testing.T) {
	const dbClone, cacheClone = "alpha-db-staging-conn", "alpha-cache-staging-conn"
	cases := []struct {
		name       string
		subscribed []string
		want       []string
		notWant    []string
	}{
		{"subscribed to db only", []string{"db"}, []string{dbClone}, []string{cacheClone}},
		{"subscribed to nothing", []string{}, nil, []string{dbClone, cacheClone}},
		{"nil subscription mounts all", nil, []string{dbClone, cacheClone}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := fakeService(t,
				seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
				seedService("alpha", "web", kube.KusoServiceSpec{Runtime: "dockerfile", Port: 3000, SubscribedAddons: tc.subscribed}),
				seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
				seedAddon("alpha", "db", "postgres"),
				seedAddon("alpha", "cache", "redis"),
			)
			s.AddonConnSecrets = func(ctx context.Context, project string) ([]string, error) {
				return []string{"alpha-db-conn", "alpha-cache-conn", "alpha-shared"}, nil
			}
			var got struct {
				scope   string
				kinds   []string
				seedAll bool
				called  bool
			}
			s.EnvAddons = fakeEnvAddons([]string{dbClone, cacheClone}, &got)

			env, err := s.AddEnvironment(context.Background(), "alpha", "web", CreateEnvRequest{Name: "staging", Branch: "staging"})
			if err != nil {
				t.Fatalf("AddEnvironment: %v", err)
			}
			efs := env.Spec.EnvFromSecrets
			for _, w := range tc.want {
				if !contains(efs, w) {
					t.Errorf("want %s mounted: %v", w, efs)
				}
			}
			for _, nw := range tc.notWant {
				if contains(efs, nw) {
					t.Errorf("%s must not be mounted: %v", nw, efs)
				}
			}
			for _, prod := range []string{"alpha-db-conn", "alpha-cache-conn"} {
				if contains(efs, prod) {
					t.Errorf("production conn %s leaked into staging env: %v", prod, efs)
				}
			}
		})
	}
}

// A clone whose name doesn't derive from its source (the source was renamed
// after the clone was minted) must still be targeted: AddEnvironment has to
// rescope by EnvAddons' authoritative source→clone map, not by name. The
// name-derived form looks for alpha-maindb-staging-conn, misses, and leaves
// staging's DATABASE_URL on the PRODUCTION database.
func TestAddEnvironment_RescopesByOriginForRenamedSource(t *testing.T) {
	dbRef := func(name string) map[string]any {
		return map[string]any{"secretKeyRef": map[string]any{"name": name, "key": "DATABASE_URL"}}
	}
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{
			Runtime: "dockerfile", Port: 3000,
			EnvVars: []kube.KusoEnvVar{{Name: "DATABASE_URL", ValueFrom: dbRef("alpha-maindb-conn")}},
		}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
		seedAddon("alpha", "maindb", "postgres"),
	)
	s.AddonConnSecrets = func(ctx context.Context, project string) ([]string, error) {
		return []string{"alpha-maindb-conn"}, nil
	}
	s.EnvAddons = func(ctx context.Context, project, envScope string, kinds []string, seedAll bool) ([]string, map[string]string, error) {
		return []string{"alpha-db-staging-conn"}, map[string]string{"alpha-maindb-conn": "alpha-db-staging-conn"}, nil
	}

	env, err := s.AddEnvironment(context.Background(), "alpha", "web", CreateEnvRequest{Name: "staging", Branch: "staging"})
	if err != nil {
		t.Fatalf("AddEnvironment: %v", err)
	}
	for _, e := range env.Spec.EnvVars {
		if e.Name != "DATABASE_URL" {
			continue
		}
		skr, _ := e.ValueFrom["secretKeyRef"].(map[string]any)
		if name, _ := skr["name"].(string); name != "alpha-db-staging-conn" {
			t.Fatalf("DATABASE_URL -> %q, want the clone alpha-db-staging-conn", name)
		}
		return
	}
	t.Fatalf("DATABASE_URL missing: %+v", env.Spec.EnvVars)
}

// Only refs to conns this env stopped mounting are moved. A conn that isn't
// one of the project's addon conns stays in envFromSecrets, so its explicit
// ref is left alone even if EnvAddons reports a clone for it.
func TestAddEnvironment_RescopeLeavesUndroppedConnRefs(t *testing.T) {
	ref := map[string]any{"secretKeyRef": map[string]any{"name": "alpha-cache-conn", "key": "REDIS_URL"}}
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{
			Runtime: "dockerfile", Port: 3000,
			EnvVars: []kube.KusoEnvVar{{Name: "REDIS_URL", ValueFrom: ref}},
		}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
		seedAddon("alpha", "pg", "postgres"),
	)
	s.AddonConnSecrets = func(ctx context.Context, project string) ([]string, error) {
		return []string{"alpha-pg-conn"}, nil
	}
	s.EnvAddons = func(ctx context.Context, project, envScope string, kinds []string, seedAll bool) ([]string, map[string]string, error) {
		return []string{"alpha-pg-staging-conn", "alpha-cache-staging-conn"},
			map[string]string{"alpha-pg-conn": "alpha-pg-staging-conn", "alpha-cache-conn": "alpha-cache-staging-conn"}, nil
	}
	env, err := s.AddEnvironment(context.Background(), "alpha", "web", CreateEnvRequest{Name: "staging", Branch: "staging"})
	if err != nil {
		t.Fatalf("AddEnvironment: %v", err)
	}
	for _, e := range env.Spec.EnvVars {
		if e.Name == "REDIS_URL" {
			skr, _ := e.ValueFrom["secretKeyRef"].(map[string]any)
			if name, _ := skr["name"].(string); name != "alpha-cache-conn" {
				t.Fatalf("REDIS_URL -> %q, want alpha-cache-conn untouched", name)
			}
			return
		}
	}
	t.Fatalf("REDIS_URL missing: %+v", env.Spec.EnvVars)
}

// --seed-from was validated and then dropped: the cloner always seeded
// from production.
func TestAddEnvironment_PassesSeedFromToCloner(t *testing.T) {
	stagingPG := seedAddon("alpha", "pg-staging", "postgres")
	stagingPG.obj.SetLabels(map[string]string{labelProject: "alpha", labelEnv: "staging"})
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Runtime: "dockerfile", Port: 3000}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
		seedAddon("alpha", "pg", "postgres"),
		stagingPG,
	)
	s.AddonConnSecrets = func(ctx context.Context, project string) ([]string, error) {
		return []string{"alpha-pg-conn"}, nil
	}
	var gotSeedFrom string
	s.EnvAddonsFrom = func(_ context.Context, _, _ string, _ []string, seedAll bool, seedFrom string) ([]string, map[string]string, error) {
		if !seedAll {
			t.Errorf("seedAll = false with --seed-from")
		}
		gotSeedFrom = seedFrom
		return []string{"alpha-pg-qa-conn"}, map[string]string{"alpha-pg-conn": "alpha-pg-qa-conn"}, nil
	}
	if _, err := s.AddEnvironment(context.Background(), "alpha", "web", CreateEnvRequest{Name: "qa", Branch: "qa", SeedFrom: "staging"}); err != nil {
		t.Fatalf("AddEnvironment: %v", err)
	}
	if gotSeedFrom != "staging" {
		t.Fatalf("seedFrom passed to cloner = %q, want staging", gotSeedFrom)
	}
}
