package projects

import (
	"context"
	"testing"
)

// TestCreate_DefaultsToPerProjectNamespace is the HIGH-7 guard: a project
// created without an explicit namespace must land in its own derived
// namespace (kuso-<name>), not the shared home namespace — that's what
// restores PodSecurity=restricted + usable ResourceQuota for tenants.
func TestCreate_DefaultsToPerProjectNamespace(t *testing.T) {
	t.Parallel()
	s := fakeService(t)
	created, err := s.Create(context.Background(), CreateProjectRequest{Name: "shop"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got, want := created.Spec.Namespace, "kuso-shop"; got != want {
		t.Fatalf("derived namespace = %q, want %q", got, want)
	}
}

// TestCreate_HonorsExplicitNamespace: an operator that pins a namespace
// keeps it (the derive only fills an empty value).
func TestCreate_HonorsExplicitNamespace(t *testing.T) {
	t.Parallel()
	s := fakeService(t)
	created, err := s.Create(context.Background(), CreateProjectRequest{Name: "shop", Namespace: "custom-ns"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := created.Spec.Namespace; got != "custom-ns" {
		t.Fatalf("explicit namespace overridden: got %q, want custom-ns", got)
	}
}

func TestDerivedProjectNamespace(t *testing.T) {
	t.Parallel()
	if got := derivedProjectNamespace("shop"); got != "kuso-shop" {
		t.Fatalf("derivedProjectNamespace(shop) = %q", got)
	}
}
