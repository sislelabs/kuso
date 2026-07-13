package addons

import (
	"context"
	"errors"
	"testing"
)

// ---- finding 5: qualified-name cross-project access ----------------------
//
// CRName accepts already-qualified input ("<project>-<name>"). With
// overlapping project names — "foo" and "foo-bar" — a member of foo
// passing addon="foo-bar-pg" resolves to the CR "foo-bar-pg", which is
// foo-bar's addon "pg". Every path that fetches by CRName must verify
// the FETCHED CR's spec.project (addonOwnedByProject) before acting.

func overlapAddonFixture(t *testing.T) *Service {
	t.Helper()
	return fakeService(t,
		seedProj("foo"),
		seedProj("foo-bar"),
		seedAddon("foo-bar", "pg", "postgres"),
	)
}

func TestGetOwned_RejectsCrossProjectQualifiedName(t *testing.T) {
	t.Parallel()
	s := overlapAddonFixture(t)
	ctx := context.Background()

	if _, err := s.GetOwned(ctx, "foo", "foo-bar-pg"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetOwned(foo, foo-bar-pg): want ErrNotFound, got %v", err)
	}
	// The legitimate owner resolves it in both forms.
	if _, err := s.GetOwned(ctx, "foo-bar", "pg"); err != nil {
		t.Errorf("GetOwned(foo-bar, pg): %v", err)
	}
	if _, err := s.GetOwned(ctx, "foo-bar", "foo-bar-pg"); err != nil {
		t.Errorf("GetOwned(foo-bar, foo-bar-pg): %v", err)
	}
	// Missing addon stays a plain not-found.
	if _, err := s.GetOwned(ctx, "foo", "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetOwned(foo, nope): want ErrNotFound, got %v", err)
	}
}

func TestUpdate_RejectsCrossProjectQualifiedName(t *testing.T) {
	t.Parallel()
	s := overlapAddonFixture(t)
	v := "17"
	_, err := s.Update(context.Background(), "foo", "foo-bar-pg", UpdateAddonRequest{Version: &v})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Update: want ErrNotFound, got %v", err)
	}
	// Victim CR untouched.
	cr, gerr := s.GetOwned(context.Background(), "foo-bar", "pg")
	if gerr != nil {
		t.Fatalf("victim addon gone: %v", gerr)
	}
	if cr.Spec.Version == v {
		t.Errorf("victim addon mutated: %+v", cr.Spec)
	}
}

func TestSetPlacement_RejectsCrossProjectQualifiedName(t *testing.T) {
	t.Parallel()
	s := overlapAddonFixture(t)
	if err := s.SetPlacement(context.Background(), "foo", "foo-bar-pg", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetPlacement: want ErrNotFound, got %v", err)
	}
}

func TestDelete_RejectsCrossProjectQualifiedName(t *testing.T) {
	t.Parallel()
	s := overlapAddonFixture(t)
	if err := s.Delete(context.Background(), "foo", "foo-bar-pg"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete: want ErrNotFound, got %v", err)
	}
	if _, err := s.GetOwned(context.Background(), "foo-bar", "pg"); err != nil {
		t.Fatalf("victim addon deleted: %v", err)
	}
}

func TestResyncExternal_RejectsCrossProjectQualifiedName(t *testing.T) {
	t.Parallel()
	s := overlapAddonFixture(t)
	if err := s.ResyncExternal(context.Background(), "foo", "foo-bar-pg"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ResyncExternal: want ErrNotFound, got %v", err)
	}
}
