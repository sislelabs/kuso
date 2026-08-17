package addons

import "testing"

// The database name reaches DDL that runs as the addon's Postgres
// SUPERUSER (EnsureBrowserRole → psql -c). Identifiers can't be bound
// as query parameters, so the boundary validator is the first of two
// defenses (pq.QuoteIdentifier at the sink is the second). These cases
// are the payload shapes that previously would have escaped the
// GRANT ... ON DATABASE "%s" interpolation.
func TestValidDatabaseName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		// Empty is legal: the field is optional and the charts derive a
		// default from the addon name when unset.
		{"empty is allowed", "", true},

		{"plain", "appdb", true},
		{"underscore lead", "_internal", true},
		{"digits and underscore", "app_db_2", true},
		{"dollar is legal in pg idents", "app$db", true},
		{"uppercase", "AppDB", true},
		{"max length 63", string(make63()), true},

		{"leading digit", "1db", false},
		{"hyphen", "app-db", false},
		{"space", "app db", false},
		{"64 chars exceeds NAMEDATALEN", string(make63()) + "x", false},

		// Injection payloads: each of these, unquoted, would break out
		// of the GRANT statement and append attacker DDL that runs as
		// superuser — COPY ... TO PROGRAM is shell execution in the pod.
		{"quote breakout", `db" TO kuso_browser; COPY x TO PROGRAM 'sh -c id'; --`, false},
		{"embedded double quote", `db"x`, false},
		{"statement separator", "db; DROP DATABASE other", false},
		{"comment injection", "db--", false},
		{"newline injection", "db\nGRANT ALL", false},
		{"backslash", `db\`, false},
		{"single quote", "db'x", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := validDatabaseName(tc.in); got != tc.want {
				t.Errorf("validDatabaseName(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// make63 builds a maximum-length (63-byte) valid identifier.
func make63() []byte {
	b := make([]byte, 63)
	b[0] = 'a'
	for i := 1; i < 63; i++ {
		b[i] = 'b'
	}
	return b
}

// storageSize is rendered into 15 addon manifests. Emitted raw, a YAML
// block scalar carrying a newline + "---" injects a COMPLETE separate
// Kubernetes object into the helm release — a privileged hostPID Pod
// was reproducible this way, and Helm's kind-ordering installs it
// BEFORE the StatefulSet. On a namespace without PodSecurity labels
// (the home namespace deliberately has none, so buildkitd can run
// privileged) that reaches node compromise from a project-editor role.
func TestValidateStorageSize(t *testing.T) {
	valid := []string{
		"",       // optional — the chart derives it from the t-shirt size
		"10Gi",
		"500Mi",
		"1Ti",
		"2G",
		"100",
		"1.5Gi",
	}
	for _, s := range valid {
		t.Run("valid/"+s, func(t *testing.T) {
			if err := validateStorageSize(s); err != nil {
				t.Errorf("validateStorageSize(%q) = %v, want nil", s, err)
			}
		})
	}

	invalid := []struct {
		name string
		in   string
	}{
		// The actual manifest-injection payload.
		{"manifest injection via newline", "10Gi\n---\napiVersion: v1\nkind: Pod\nmetadata:\n  name: pwned"},
		{"bare newline", "10Gi\n"},
		{"carriage return", "10Gi\r\nfoo"},
		{"leading space", " 10Gi"},
		{"trailing space", "10Gi "},
		{"internal space", "10 Gi"},
		{"tab", "10Gi\tx"},
		{"not a quantity", "big"},
		{"yaml fragment", "{}"},
		{"negative-ish garbage", "10Gi; rm -rf /"},
	}
	for _, tc := range invalid {
		t.Run("invalid/"+tc.name, func(t *testing.T) {
			if err := validateStorageSize(tc.in); err == nil {
				t.Errorf("validateStorageSize(%q) = nil, want error", tc.in)
			}
		})
	}
}
