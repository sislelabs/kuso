package projects

import (
	"context"
	"errors"
	"slices"
	"testing"

	"kuso/server/internal/kube"
)

// WEB-14: the subscription PUTs replace the whole list, so a client editing
// from a stale read dropped another writer's change. An optional `expected`
// list (what the client read) turns that into ErrConflict; nil keeps the old
// unconditional replace for existing clients.
func TestSubscriptionPutsHonourExpectedPrecondition(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := fakeServiceWithSecrets(t, nil,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
	)
	s.AddonConnSecrets = func(context.Context, string) ([]string, error) {
		return []string{"alpha-db-conn", "alpha-cache-conn"}, nil
	}

	// Unset list reads as "every addon", so that is what a client expects.
	if _, err := s.SetSubscribedAddons(ctx, "alpha", "web", []string{"db"}, []string{"db", "cache"}); err != nil {
		t.Fatalf("matching precondition on an unset list: %v", err)
	}
	if _, err := s.SetSubscribedAddons(ctx, "alpha", "web", []string{"cache"}, []string{"db", "cache"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale precondition = %v, want ErrConflict", err)
	}
	if _, err := s.SetSubscribedAddons(ctx, "alpha", "web", []string{"db", "cache"}, []string{" db", "db"}); err != nil {
		t.Fatalf("precondition is compared as a set: %v", err)
	}
	if _, err := s.SetSubscribedAddons(ctx, "alpha", "web", []string{}, nil); err != nil {
		t.Fatalf("no precondition must replace unconditionally: %v", err)
	}
	if _, err := s.SetSubscribedAddons(ctx, "alpha", "web", []string{"db"}, []string{}); err != nil {
		t.Fatalf("empty precondition matches an empty list: %v", err)
	}

	if _, err := s.SetSharedEnvKeys(ctx, "alpha", "web", []string{"A"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetSharedEnvKeys(ctx, "alpha", "web", []string{"B"}, []string{"B"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale shared-key precondition = %v, want ErrConflict", err)
	}
	if _, err := s.SetSharedEnvKeys(ctx, "alpha", "web", []string{"A", "B"}, []string{"A"}); err != nil {
		t.Fatalf("matching shared-key precondition: %v", err)
	}
	svc, err := s.GetService(ctx, "alpha", "web")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(svc.Spec.SubscribedAddons, []string{"db"}) || !slices.Equal(svc.Spec.SharedEnvKeys, []string{"A", "B"}) {
		t.Fatalf("spec after puts = addons %v keys %v", svc.Spec.SubscribedAddons, svc.Spec.SharedEnvKeys)
	}
}
