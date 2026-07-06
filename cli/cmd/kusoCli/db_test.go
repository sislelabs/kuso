package kusoCli

import "testing"

// The server's GET /addons/{addon}/secret returns {"values": {...}} (see
// handlers.AddonsHandler.Secret). A regression earlier decoded the body
// straight into map[string]string, which fails on the wrapped shape with
// "cannot unmarshal object into Go value of type string". These tests pin the
// wrapper as the supported contract while keeping the legacy flat shape working.
func TestDecodeAddonSecret(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantKey string
		wantVal string
		wantErr bool
	}{
		{
			name:    "wrapped values (current server)",
			body:    `{"values":{"DATABASE_URL":"postgres://u:p@scaffold-db:5432/scaffold?sslmode=require","POSTGRES_DB":"scaffold"}}`,
			wantKey: "DATABASE_URL",
			wantVal: "postgres://u:p@scaffold-db:5432/scaffold?sslmode=require",
		},
		{
			name:    "legacy flat map",
			body:    `{"DATABASE_URL":"postgres://u:p@host:5432/db","REDIS_URL":""}`,
			wantKey: "DATABASE_URL",
			wantVal: "postgres://u:p@host:5432/db",
		},
		{
			// Older servers (e.g. v0.17.x against a newer CLI) mirror the env-list
			// shape: each value is an OBJECT {"value": "...", "type": "secret"}.
			// The old decoder blew up here with "cannot unmarshal object into Go
			// value of type string" — this is the live bug that motivated the fix.
			name:    "wrapped object-valued (older server)",
			body:    `{"values":{"DATABASE_URL":{"value":"postgres://u:p@host:5432/db","type":"secret"}}}`,
			wantKey: "DATABASE_URL",
			wantVal: "postgres://u:p@host:5432/db",
		},
		{
			name:    "flat object-valued",
			body:    `{"DATABASE_URL":{"value":"postgres://u:p@host:5432/db"}}`,
			wantKey: "DATABASE_URL",
			wantVal: "postgres://u:p@host:5432/db",
		},
		{
			name:    "malformed json",
			body:    `{"values":`,
			wantErr: true,
		},
		{
			name:    "no usable values",
			body:    `{"values":{"DATABASE_URL":{"no_value_here":true}}}`,
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeAddonSecret([]byte(tc.body))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got nil (parsed %v)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("decodeAddonSecret: unexpected error: %v", err)
			}
			if got[tc.wantKey] != tc.wantVal {
				t.Fatalf("key %q = %q, want %q", tc.wantKey, got[tc.wantKey], tc.wantVal)
			}
		})
	}
}

func TestLocalDSNFromSecret(t *testing.T) {
	cases := []struct {
		name     string
		secret   map[string]string
		wantKind string
		wantDSN  string
	}{
		{
			name:     "postgres",
			secret:   map[string]string{"DATABASE_URL": "postgres://u:p@db:5432/app?sslmode=require"},
			wantKind: "postgres",
			wantDSN:  "postgres://u:p@127.0.0.1:15432/app?sslmode=require",
		},
		{
			name:     "redis",
			secret:   map[string]string{"REDIS_URL": "redis://:pw@cache:6379/0"},
			wantKind: "redis",
			wantDSN:  "redis://:pw@127.0.0.1:15432/0",
		},
		{
			name:     "clickhouse prefers HTTP url over native",
			secret:   map[string]string{"CLICKHOUSE_URL": "http://default:pw@ch:8123/analytics", "CLICKHOUSE_NATIVE_URL": "clickhouse://default:pw@ch:9000/analytics"},
			wantKind: "clickhouse",
			wantDSN:  "http://default:pw@127.0.0.1:15432/analytics",
		},
		{
			name:     "postgres wins when both present (canonical order)",
			secret:   map[string]string{"DATABASE_URL": "postgres://u:p@db:5432/app", "CLICKHOUSE_URL": "http://d:pw@ch:8123/a"},
			wantKind: "postgres",
			wantDSN:  "postgres://u:p@127.0.0.1:15432/app",
		},
		{
			name:     "no usable key",
			secret:   map[string]string{"SOMETHING_ELSE": "x"},
			wantKind: "",
			wantDSN:  "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, dsn := localDSNFromSecret(tc.secret, 15432)
			if kind != tc.wantKind {
				t.Errorf("kind = %q, want %q", kind, tc.wantKind)
			}
			if dsn != tc.wantDSN {
				t.Errorf("dsn = %q, want %q", dsn, tc.wantDSN)
			}
		})
	}
}

func TestClientForKind(t *testing.T) {
	cases := map[string]string{
		"postgres":   "psql",
		"redis":      "redis-cli",
		"mongo":      "mongosh",
		"clickhouse": "", // no native client on the HTTP tunnel — runClientFor uses the built-in HTTP shell
		"unknown":    "",
	}
	for kind, want := range cases {
		if got := clientForKind(kind); got != want {
			t.Errorf("clientForKind(%q) = %q, want %q", kind, got, want)
		}
	}
}
