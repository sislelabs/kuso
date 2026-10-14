package handlers

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// A `<ts>.db-<name>.sql.gz` sibling is a --clean dump of logical DB <name>;
// restoring it must target <name>, never the conn Secret's POSTGRES_DB.
func TestSiblingDumpDB(t *testing.T) {
	t.Parallel()
	cases := []struct {
		kind, key, want string
		wantErr         bool
	}{
		{"postgres", "p/p-pg/20261014T030000Z.sql.gz", "", false},
		{"postgres", "p/p-pg/20261014T030000Z.db-acme_pg.sql.gz", "acme_pg", false},
		{"postgres", "p/p-pg/20261014T030000Z.db-bad;name.sql.gz", "", true},
		{"postgres", "p/p-pg/20261014T030000Z.db-.sql.gz", "", true},
		{"mysql", "p/p-my/20261014T030000Z.db-acme.sql.gz", "", true},
		{"mysql", "p/p-my/20261014T030000Z.sql.gz", "", false},
	}
	for _, tc := range cases {
		got, err := siblingDumpDB(tc.kind, tc.key)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("siblingDumpDB(%q, %q) = %q, %v; want %q, err=%v", tc.kind, tc.key, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestOverrideEnv_ReplacesSecretBackedDB(t *testing.T) {
	t.Parallel()
	env := overrideEnv(restoreConnEnv("postgres", "p-pg"), corev1.EnvVar{Name: "POSTGRES_DB", Value: "acme_pg"})
	n := 0
	for _, e := range env {
		if e.Name != "POSTGRES_DB" {
			continue
		}
		n++
		if e.Value != "acme_pg" || e.ValueFrom != nil {
			t.Errorf("POSTGRES_DB = %+v, want literal acme_pg", e)
		}
	}
	if n != 1 {
		t.Errorf("POSTGRES_DB appears %d times, want 1", n)
	}
}
