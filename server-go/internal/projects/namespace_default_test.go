package projects

import (
	"context"
	"testing"
)

func TestCreate_Namespace(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		explicit string
		want     string
	}{
		// HIGH-7 guard: a project created without an explicit namespace must
		// land in its own derived namespace (kuso-<name>), not the shared home
		// namespace — that's what restores PodSecurity=restricted + usable
		// ResourceQuota for tenants.
		{"DefaultsToPerProjectNamespace", "", "kuso-shop"},
		// An operator that pins a namespace keeps it (the derive only fills an
		// empty value).
		{"HonorsExplicitNamespace", "custom-ns", "custom-ns"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := fakeService(t)
			created, err := s.Create(context.Background(), CreateProjectRequest{Name: "shop", Namespace: tc.explicit})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if got := created.Spec.Namespace; got != tc.want {
				t.Fatalf("namespace = %q, want %q", got, tc.want)
			}
		})
	}
}
