package addons

import (
	"context"
	"testing"

	"kuso/server/internal/kube"
)

// Deleting an env clone backed by the shared instance server must drop its
// database: nothing else references it afterwards, and before this was
// wired up 16 clone databases leaked on production. shouldDropInstanceDB is
// tested on its own; this pins the call from Delete. A project's own addon
// keeps its database (retain semantics), so the same flow must not drop it.
func TestDelete_InstanceCloneDropsDB_ProjectAddonKeepsIt(t *testing.T) {
	cases := []struct {
		name     string
		addon    string
		labels   map[string]string
		wantDrop bool
	}{
		{"staging clone", "db-staging", map[string]string{kube.LabelEnv: "staging"}, true},
		{"preview clone", "db-pr-3", map[string]string{kube.LabelEnv: "preview-pr-3", "kuso.sislelabs.com/preview-pr": "3"}, true},
		{"project addon", "db", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := uniqueProject(t)
			s, adminDSN := instancePGService(t, p)
			ident := pgIdentifier(p, tc.addon)
			dropTestIdent(t, adminDSN, ident)

			if _, err := s.Add(context.Background(), p, CreateAddonRequest{
				Name: tc.addon, Kind: "postgres", UseInstanceAddon: "pg", ExtraLabels: tc.labels,
			}); err != nil {
				t.Fatalf("add: %v", err)
			}
			if !dbExists(t, adminDSN, ident) {
				t.Fatalf("fixture: add did not create %s", ident)
			}
			if err := s.Delete(context.Background(), p, tc.addon); err != nil {
				t.Fatalf("delete: %v", err)
			}
			if got := !dbExists(t, adminDSN, ident); got != tc.wantDrop {
				t.Fatalf("after delete, %s dropped = %v, want %v", ident, got, tc.wantDrop)
			}
		})
	}
}
