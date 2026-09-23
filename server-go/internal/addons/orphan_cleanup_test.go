package addons

import (
	"testing"

	"kuso/server/internal/kube"
)

// An instance-pg addon's "data" is a logical DATABASE on the shared
// server, not a StatefulSet PVC. Deleting the CR without dropping that
// database leaks it: nothing references it, nothing lists it, and it
// keeps consuming space on the shared instance forever.
//
// The drop used to be gated on the preview-pr label alone, so per-PR
// clones were reclaimed but NAMED env clones (env-group staging/qa,
// labelled kuso.sislelabs.com/env=<scope>) were not. That leaked 16
// databases (~155MB) on this cluster before anyone noticed, including
// bukvite_db_staging and bukvite30_db.
//
// A PROJECT'S OWN addon must still retain its data — that matches the
// native-addon PVC retain semantics and protects against an accidental
// production delete.
func TestShouldDropInstanceDB(t *testing.T) {
	cases := []struct {
		name   string
		labels map[string]string
		want   bool
		why    string
	}{
		{
			name:   "per-PR preview clone",
			labels: map[string]string{"kuso.sislelabs.com/preview-pr": "42"},
			want:   true,
			why:    "ephemeral by construction",
		},
		{
			name:   "named env-group clone (the leak)",
			labels: map[string]string{kube.LabelEnv: "staging"},
			want:   true,
			why:    "a clone's data is disposable; it is rebuilt from the source on re-create",
		},
		{
			name:   "named env clone, qa scope",
			labels: map[string]string{kube.LabelEnv: "qa"},
			want:   true,
			why:    "same as staging",
		},
		{
			name:   "project's own production addon",
			labels: map[string]string{},
			want:   false,
			why:    "RETAIN — an accidental delete must not nuke production data",
		},
		{
			name:   "production-labelled addon is still the project's own",
			labels: map[string]string{kube.LabelEnv: "production"},
			want:   false,
			why:    "production is not a clone",
		},
		{
			name:   "nil labels",
			labels: nil,
			want:   false,
			why:    "absence of a clone marker means it is the real thing",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldDropInstanceDB(tc.labels); got != tc.want {
				t.Fatalf("shouldDropInstanceDB(%v) = %v, want %v (%s)",
					tc.labels, got, tc.want, tc.why)
			}
		})
	}
}
