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

	got, err := s.GetEnvScoped(context.Background(), "alpha", "web", "production")
	if err != nil {
		t.Fatalf("GetEnvScoped: %v", err)
	}
	if len(got) != 1 || got[0].Name != "DATABASE_URL" || got[0].Value != "postgres://only-on-prod" {
		t.Errorf("got %+v, want the single production override", got)
	}
}

func TestGetEnvScoped_UnknownEnvIsNotFound(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{}),
	)
	_, err := s.GetEnvScoped(context.Background(), "alpha", "web", "nope")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}
