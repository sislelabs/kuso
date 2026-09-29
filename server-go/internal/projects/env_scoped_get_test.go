package projects

import (
	"context"
	"errors"
	"testing"

	"kuso/server/internal/kube"
)

// `kuso env set --env production KEY=v` writes an override onto the env CR.
// `kuso env list` reads the SERVICE spec, so an override set that way was
// invisible everywhere — not in the CLI, not in the Variables tab. On tickero
// DATABASE_URL lived only as a production override, and the honest answer to
// "why is there no database url here" was that nothing could show it.
func TestGetEnvScoped_ReturnsEnvOverrides(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{}),
		seedEnvWithVars("alpha", "web", "production", "main", "alpha-web-production",
			kube.KusoEnvVar{Name: "DATABASE_URL", Value: "postgres://only-on-prod"}),
	)

	got, err := s.GetEnvScoped(context.Background(), "alpha", "web", "production", false)
	if err != nil {
		t.Fatalf("GetEnvScoped: %v", err)
	}
	if len(got) != 1 || got[0].Name != "DATABASE_URL" || got[0].Value != "postgres://only-on-prod" {
		t.Errorf("got %+v, want the single production override", got)
	}
}

// Propagation stamps every service var and subscribed shared key onto the env
// CR, so returning the CR's whole envVars made the Variables tab list the
// entire service twice under "Overrides on production". Only names the user
// pinned on the env (EnvOverrides) or that exist nowhere upstream are overrides.
func TestGetEnvScoped_OmitsPropagatedCopies(t *testing.T) {
	t.Parallel()
	env := seedEnvWithVars("alpha", "web", "production", "main", "alpha-web-production",
		kube.KusoEnvVar{Name: "FROM_SERVICE", Value: "same"},
		kube.KusoEnvVar{Name: "PINNED", Value: "prod-only"},
		kube.KusoEnvVar{Name: "SHARED_KEY", ValueFrom: map[string]any{
			"secretKeyRef": map[string]any{"name": "alpha-shared", "key": "SHARED_KEY"},
		}},
		kube.KusoEnvVar{Name: "NET_NEW", Value: "only-here"},
	)
	env.obj.Object["spec"].(map[string]any)["envOverrides"] = []any{"PINNED"}
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{
			EnvVars: []kube.KusoEnvVar{
				{Name: "FROM_SERVICE", Value: "same"},
				{Name: "PINNED", Value: "service-default"},
			},
			SharedEnvKeys: []string{"SHARED_KEY"},
		}),
		env,
	)

	got, err := s.GetEnvScoped(context.Background(), "alpha", "web", "production", false)
	if err != nil {
		t.Fatalf("GetEnvScoped: %v", err)
	}
	var names []string
	for _, e := range got {
		names = append(names, e.Name)
	}
	if len(names) != 2 || names[0] != "PINNED" || names[1] != "NET_NEW" {
		t.Errorf("got %v, want [PINNED NET_NEW]", names)
	}
}

func TestGetEnvScoped_UnknownEnvIsNotFound(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{}),
	)
	_, err := s.GetEnvScoped(context.Background(), "alpha", "web", "nope", false)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}
