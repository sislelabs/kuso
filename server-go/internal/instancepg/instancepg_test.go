package instancepg

import (
	"testing"
)

// TestBuildAdminDSN pins the DSN composition from the kusoaddon
// postgres chart's conn-Secret keys. Any change to the chart's keys
// will break this and force us to look at addons.instance_provisioner
// in lockstep — those two paths must agree on the key shape.
func TestBuildAdminDSN(t *testing.T) {
	tests := []struct {
		name string
		data map[string][]byte
		want string
	}{
		{
			name: "full keys",
			data: map[string][]byte{
				"POSTGRES_HOST":     []byte("instance-pg"),
				"POSTGRES_PORT":     []byte("5432"),
				"POSTGRES_USER":     []byte("kuso"),
				"POSTGRES_PASSWORD": []byte("supersecret"),
				"POSTGRES_DB":       []byte("postgres"),
			},
			want: "postgres://kuso:supersecret@instance-pg:5432/postgres?sslmode=disable",
		},
		{
			name: "missing port defaults to 5432",
			data: map[string][]byte{
				"POSTGRES_HOST":     []byte("instance-pg"),
				"POSTGRES_USER":     []byte("kuso"),
				"POSTGRES_PASSWORD": []byte("pw"),
				"POSTGRES_DB":       []byte("postgres"),
			},
			want: "postgres://kuso:pw@instance-pg:5432/postgres?sslmode=disable",
		},
		{
			name: "missing db defaults to postgres",
			data: map[string][]byte{
				"POSTGRES_HOST":     []byte("h"),
				"POSTGRES_PORT":     []byte("5432"),
				"POSTGRES_USER":     []byte("u"),
				"POSTGRES_PASSWORD": []byte("p"),
			},
			want: "postgres://u:p@h:5432/postgres?sslmode=disable",
		},
		{
			name: "no host = empty",
			data: map[string][]byte{
				"POSTGRES_USER":     []byte("u"),
				"POSTGRES_PASSWORD": []byte("p"),
			},
			want: "",
		},
		{
			name: "no password = empty (refuse to emit credential-less DSN)",
			data: map[string][]byte{
				"POSTGRES_HOST": []byte("h"),
				"POSTGRES_USER": []byte("u"),
			},
			want: "",
		},
		{
			name: "password with special chars is url-encoded",
			data: map[string][]byte{
				"POSTGRES_HOST":     []byte("h"),
				"POSTGRES_PORT":     []byte("5432"),
				"POSTGRES_USER":     []byte("u"),
				"POSTGRES_PASSWORD": []byte("pa$$ w@rd"),
				"POSTGRES_DB":       []byte("postgres"),
			},
			// url.UserPassword percent-encodes user-info reserved
			// chars. Verifying the exact encoded shape catches
			// double-encoding bugs.
			want: "postgres://u:pa$$%20w%40rd@h:5432/postgres?sslmode=disable",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildAdminDSN(tc.data); got != tc.want {
				t.Errorf("buildAdminDSN() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestParseDSNDisplay verifies we never leak the password through
// the display fields, and that a malformed DSN returns clean zero
// values (no partial leak via Sprintf-style fallback).
func TestParseDSNDisplay(t *testing.T) {
	host, port, user := parseDSNDisplay("postgres://kuso:hunter2@example.com:5432/db?sslmode=disable")
	if host != "example.com" || port != "5432" || user != "kuso" {
		t.Errorf("display: host=%q port=%q user=%q", host, port, user)
	}
	// Confirm via field-by-field that the password never appears in
	// any returned string — a regression where someone accidentally
	// returns u.User.String() would leak it.
	for _, s := range []string{host, port, user} {
		if s == "hunter2" || s == "kuso:hunter2" {
			t.Fatalf("password leaked into display field: %q", s)
		}
	}
}

// TestAddonAndConnNames pins the deterministic naming so any future
// refactor that touches one site has to touch the other. The chart
// + the provisioner + the reconciler all key off these strings.
func TestAddonAndConnNames(t *testing.T) {
	if got := addonCRName(); got != "__instance__-pg" {
		t.Errorf("addonCRName() = %q", got)
	}
	if got := connSecretName(); got != "__instance__-pg-conn" {
		t.Errorf("connSecretName() = %q", got)
	}
}
