package projects

import (
	"testing"

	"kuso/server/internal/kube"
)

// TestChooseEnvStorage locks in the "one secret primitive" storage rule:
// a plain value becomes a secret UNLESS the build needs it (publicEnv /
// buildArgs), and a ${{ ref }} stays a ref. This is what makes the
// consolidation zero-degradation for build-time env.
func TestChooseEnvStorage(t *testing.T) {
	t.Parallel()
	svc := &kube.KusoService{}
	svc.Spec.PublicEnv = []string{"NEXT_PUBLIC_API_URL"}
	svc.Spec.BuildArgs = map[string]string{"SENTRY_RELEASE": "x"}

	cases := []struct {
		name, value string
		want        EnvStorage
	}{
		// Default: an ordinary value → secret (off the CR).
		{"WETRAVEL_API_KEY", "sk_live_123", StorageSecret},
		{"SESSION_SECRET", "hunter2", StorageSecret},
		// Build-relevant names stay CR env so the build can resolve them.
		{"NEXT_PUBLIC_API_URL", "https://api.example.com", StorageCREnv},
		{"SENTRY_RELEASE", "v1.2.3", StorageCREnv},
		// A ${{ ref }} is addon/shared wiring, not a secret.
		{"DATABASE_URL", "${{ pg.URL }}", StorageRef},
		{"REDIS_URL", "${{ cache.URL }}", StorageRef},
		// Reserved runtime-only selectors must NOT be build-relevant even
		// if a user were to list them — they stay secrets, never CR-baked
		// into the build (NODE_ENV=production breaks npm installs).
		{"NODE_ENV", "production", StorageSecret},
	}
	for _, c := range cases {
		if got := chooseEnvStorage(c.name, c.value, svc); got != c.want {
			t.Errorf("chooseEnvStorage(%q,%q) = %v, want %v", c.name, c.value, got, c.want)
		}
	}
}

// TestChooseEnvStorage_NilService: no service spec → nothing is
// build-relevant, so plain values default to secret and refs stay refs.
func TestChooseEnvStorage_NilService(t *testing.T) {
	t.Parallel()
	if got := chooseEnvStorage("FOO", "bar", nil); got != StorageSecret {
		t.Errorf("nil svc plain = %v, want StorageSecret", got)
	}
	if got := chooseEnvStorage("DB", "${{ pg.URL }}", nil); got != StorageRef {
		t.Errorf("nil svc ref = %v, want StorageRef", got)
	}
}

// TestReservedBuildEnvNamesMirror guards that the projects-side mirror
// stays in lockstep with the intent (a representative sample).
func TestReservedBuildEnvNamesMirror(t *testing.T) {
	t.Parallel()
	for _, n := range []string{"NODE_ENV", "PORT", "CI", "RAILS_ENV", "PATH"} {
		if !reservedBuildEnvName(n) {
			t.Errorf("%s should be reserved (build must not see it)", n)
		}
	}
	if reservedBuildEnvName("DATABASE_URL") {
		t.Error("DATABASE_URL should not be reserved")
	}
}
