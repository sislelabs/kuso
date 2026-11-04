package builds

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"kuso/server/internal/kube"
)

type kindMap map[string]string

func (k kindMap) AddonKind(_ context.Context, _, addon string) (string, error) {
	if kind, ok := k[addon]; ok {
		return kind, nil
	}
	return "", fmt.Errorf("%w: %s", ErrNotAnAddon, addon)
}

type recordJobs struct{ addons []string }

func (r *recordJobs) CreateSnapshotJob(_ context.Context, _, addon, _, _ string) (string, error) {
	r.addons = append(r.addons, addon)
	return "key-" + addon, nil
}

func refVar(name, secret string) kube.KusoEnvVar {
	return kube.KusoEnvVar{Name: name, ValueFrom: map[string]any{"secretKeyRef": map[string]any{"name": secret, "key": name}}}
}

// DATA-05: a service reaching its DB through ${{ db.DIRECT_URL }} (an explicit
// secretKeyRef) with nothing in envFrom got no snapshot and no error.
func TestSnapshot_IncludesSecretKeyRefAddons(t *testing.T) {
	t.Parallel()
	env := &kube.KusoEnvironment{}
	env.Spec.Project = "acme"
	env.Spec.EnvFromSecrets = []string{"acme-cache-conn"}
	env.Spec.EnvVars = []kube.KusoEnvVar{
		refVar("DIRECT_URL", "acme-db-conn"),
		refVar("DATABASE_URL", "acme-db-conn"),
		refVar("API_KEY", "user-api-conn"), // a user secret that merely ends in -conn
	}
	jobs := &recordJobs{}
	s := &PredeploySnapshotter{Kinds: kindMap{"acme-db": "postgres", "acme-cache": "redis"}, Jobs: jobs}
	keys, err := s.Snapshot(context.Background(), "kuso", env)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !slices.Equal(jobs.addons, []string{"acme-db"}) || len(keys) != 1 {
		t.Fatalf("snapshotted %v (keys %v), want exactly acme-db once", jobs.addons, keys)
	}
}
