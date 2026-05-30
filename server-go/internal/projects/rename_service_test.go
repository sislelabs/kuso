package projects

import (
	"context"
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"kuso/server/internal/kube"
)

// RenameService clones a service (and its non-preview envs) under a new
// FQN, recomputes default-shape hosts, drops preview envs, then tears
// down the old. It's destructive + untested — these pin the invariants
// that matter when a user renames a live service.

// setEnvHostInSeed stamps spec.host on a seeded env so a rename test
// can assert host-rewrite behaviour (default-shape host moves; bespoke
// host is preserved).
func setEnvHostInSeed(s seed, host string) error {
	return unstructured.SetNestedField(s.obj.Object, host, "spec", "host")
}

func TestRenameService_ClonesServiceAndEnvs(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Port: 3000}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
		seedEnv("alpha", "web", "staging", "stage", "alpha-web-staging"),
	)

	created, err := s.RenameService(context.Background(), "alpha", "web", "api")
	if err != nil {
		t.Fatalf("RenameService: %v", err)
	}
	if created.Name != "alpha-api" {
		t.Errorf("renamed service name = %q, want alpha-api", created.Name)
	}
	if created.Spec.Port != 3000 {
		t.Errorf("renamed service lost spec: port = %d, want 3000", created.Spec.Port)
	}

	// New envs exist under the new short name; old envs gone.
	newEnvs := envByName(t, s, "alpha", "api")
	if _, ok := newEnvs["alpha-api-production"]; !ok {
		t.Errorf("production env not cloned: have %v", keysOf(newEnvs))
	}
	if _, ok := newEnvs["alpha-api-staging"]; !ok {
		t.Errorf("staging env not cloned: have %v", keysOf(newEnvs))
	}
	oldEnvs := envByName(t, s, "alpha", "web")
	if len(oldEnvs) != 0 {
		t.Errorf("old envs still present after rename: %v", keysOf(oldEnvs))
	}
	// Old service CR gone.
	if _, err := s.GetService(context.Background(), "alpha", "web"); err == nil {
		t.Errorf("old service still exists after rename")
	}
}

func TestRenameService_DropsPreviewEnvs(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
		seedEnv("alpha", "web", "preview", "pr-3", "alpha-web-pr-3"),
	)

	if _, err := s.RenameService(context.Background(), "alpha", "web", "api"); err != nil {
		t.Fatalf("RenameService: %v", err)
	}
	newEnvs := envByName(t, s, "alpha", "api")
	for name, env := range newEnvs {
		if env.Spec.Kind == "preview" {
			t.Errorf("preview env %q was cloned — previews must be dropped on rename", name)
		}
	}
	if _, ok := newEnvs["alpha-api-production"]; !ok {
		t.Errorf("production env not cloned alongside dropped preview")
	}
}

func TestRenameService_RewritesDefaultHostPreservesCustom(t *testing.T) {
	t.Parallel()
	// production env carries the default-shape host → should move to
	// the new name. staging carries a bespoke host → preserved verbatim.
	prodSeed := seedEnv("alpha", "web", "production", "main", "alpha-web-production")
	if err := setEnvHostInSeed(prodSeed, defaultHost("web", "alpha", "")); err != nil {
		t.Fatalf("seed prod host: %v", err)
	}
	stagingSeed := seedEnv("alpha", "web", "staging", "stage", "alpha-web-staging")
	if err := setEnvHostInSeed(stagingSeed, "custom.example.com"); err != nil {
		t.Fatalf("seed staging host: %v", err)
	}
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
		prodSeed,
		stagingSeed,
	)

	if _, err := s.RenameService(context.Background(), "alpha", "web", "api"); err != nil {
		t.Fatalf("RenameService: %v", err)
	}
	newEnvs := envByName(t, s, "alpha", "api")
	if got := newEnvs["alpha-api-production"].Spec.Host; got != defaultHost("api", "alpha", "") {
		t.Errorf("production host = %q, want default-shape %q", got, defaultHost("api", "alpha", ""))
	}
	if got := newEnvs["alpha-api-staging"].Spec.Host; got != "custom.example.com" {
		t.Errorf("staging host = %q, want preserved custom.example.com", got)
	}
}

func TestRenameService_RefusesSameName(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
	)
	if _, err := s.RenameService(context.Background(), "alpha", "web", "web"); !errors.Is(err, ErrInvalid) {
		t.Errorf("same-name rename: got %v, want ErrInvalid", err)
	}
}

func TestRenameService_RefusesDuplicateTarget(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
		seedService("alpha", "api", kube.KusoServiceSpec{Project: "alpha"}),
	)
	if _, err := s.RenameService(context.Background(), "alpha", "web", "api"); !errors.Is(err, ErrConflict) {
		t.Errorf("rename onto existing service: got %v, want ErrConflict", err)
	}
}

func keysOf(m map[string]kube.KusoEnvironment) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
