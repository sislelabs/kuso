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
			name:    "malformed json",
			body:    `{"values":`,
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
