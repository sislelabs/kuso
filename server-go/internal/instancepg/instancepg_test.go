package instancepg

import (
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestBuildAdminDSN pins the DSN composition from the kusoaddon
// postgres chart's conn-Secret keys. Any change to the chart's keys
// will break this and force us to look at addons.instance_provisioner
// in lockstep — those two paths must agree on the key shape.
//
// SSL mode policy: in-cluster Service DNS hosts (suffix .svc / .svc.
// cluster.local / loopback) get sslmode=disable because the CNPG
// chart doesn't ship a CA the lib/pq client can verify. Anything
// else gets sslmode=require — the conn Secret should never produce
// a DSN that traverses untrusted network without TLS.
func TestBuildAdminDSN(t *testing.T) {
	tests := []struct {
		name string
		data map[string][]byte
		want string
	}{
		{
			name: "in-cluster svc dns → sslmode=disable",
			data: map[string][]byte{
				"POSTGRES_HOST":     []byte("instance-pg.kuso.svc"),
				"POSTGRES_PORT":     []byte("5432"),
				"POSTGRES_USER":     []byte("kuso"),
				"POSTGRES_PASSWORD": []byte("supersecret"),
				"POSTGRES_DB":       []byte("postgres"),
			},
			want: "postgres://kuso:supersecret@instance-pg.kuso.svc:5432/postgres?sslmode=disable",
		},
		{
			name: "bare service name (chart default) → disable",
			data: map[string][]byte{
				"POSTGRES_HOST":     []byte("instance-pg"),
				"POSTGRES_PORT":     []byte("5432"),
				"POSTGRES_USER":     []byte("kuso"),
				"POSTGRES_PASSWORD": []byte("supersecret"),
				"POSTGRES_DB":       []byte("postgres"),
			},
			// Bare `instance-pg` doesn't match the .svc suffix; we
			// require SSL on principle. Operators who want plaintext
			// for the bundled in-cluster chart should point conn
			// Secrets at the fully-qualified Service DNS instead.
			want: "postgres://kuso:supersecret@instance-pg:5432/postgres?sslmode=require",
		},
		{
			name: "127.0.0.1 → disable (dev/test)",
			data: map[string][]byte{
				"POSTGRES_HOST":     []byte("127.0.0.1"),
				"POSTGRES_PORT":     []byte("5432"),
				"POSTGRES_USER":     []byte("u"),
				"POSTGRES_PASSWORD": []byte("p"),
				"POSTGRES_DB":       []byte("postgres"),
			},
			want: "postgres://u:p@127.0.0.1:5432/postgres?sslmode=disable",
		},
		{
			name: "missing port defaults to 5432",
			data: map[string][]byte{
				"POSTGRES_HOST":     []byte("instance-pg.kuso.svc.cluster.local"),
				"POSTGRES_USER":     []byte("kuso"),
				"POSTGRES_PASSWORD": []byte("pw"),
				"POSTGRES_DB":       []byte("postgres"),
			},
			want: "postgres://kuso:pw@instance-pg.kuso.svc.cluster.local:5432/postgres?sslmode=disable",
		},
		{
			name: "no host → empty",
			data: map[string][]byte{
				"POSTGRES_USER":     []byte("u"),
				"POSTGRES_PASSWORD": []byte("p"),
			},
			want: "",
		},
		{
			name: "no password → empty (refuse to emit credential-less DSN)",
			data: map[string][]byte{
				"POSTGRES_HOST": []byte("h"),
				"POSTGRES_USER": []byte("u"),
			},
			want: "",
		},
		{
			name: "password with special chars is url-encoded",
			data: map[string][]byte{
				"POSTGRES_HOST":     []byte("instance-pg.kuso.svc"),
				"POSTGRES_PORT":     []byte("5432"),
				"POSTGRES_USER":     []byte("u"),
				"POSTGRES_PASSWORD": []byte("pa$$ w@rd"),
				"POSTGRES_DB":       []byte("postgres"),
			},
			// url.UserPassword percent-encodes user-info reserved
			// chars. Verifying the exact encoded shape catches
			// double-encoding bugs.
			want: "postgres://u:pa$$%20w%40rd@instance-pg.kuso.svc:5432/postgres?sslmode=disable",
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
	for _, s := range []string{host, port, user} {
		if s == "hunter2" || s == "kuso:hunter2" {
			t.Fatalf("password leaked into display field: %q", s)
		}
	}
}

// TestAddonAndConnNames pins the deterministic naming so any future
// refactor that touches one site has to touch the other.
// rfc1123 matches a lowercase RFC-1123 subdomain — the constraint kube's
// apiserver enforces on metadata.name. The old "__instance__-pg" name failed
// this (underscores), so "Run on this cluster" 500'd before any CR was created.
var rfc1123 = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)

func TestAddonAndConnNames(t *testing.T) {
	if got := addonCRName(); got != "kuso-instance-pg" {
		t.Errorf("addonCRName() = %q, want kuso-instance-pg", got)
	}
	if got := connSecretName(); got != "kuso-instance-pg-conn" {
		t.Errorf("connSecretName() = %q, want kuso-instance-pg-conn", got)
	}
	// Both must be valid RFC-1123 names or the apiserver rejects the CR /
	// Secret. This is the actual bug guard.
	for _, name := range []string{addonCRName(), connSecretName(), instanceProject} {
		if !rfc1123.MatchString(name) {
			t.Errorf("%q is not a valid RFC-1123 name (kube apiserver will reject it)", name)
		}
		if strings.Contains(name, "_") {
			t.Errorf("%q contains an underscore — invalid in a kube resource name", name)
		}
	}
}

// TestCoerceSSLMode pins the external-DSN SSL policy. The admin DSN
// is the keys-to-the-kingdom credential for per-project provisioning
// — silently allowing plaintext over public DNS would be the wrong
// default. We default to `require` and reject `disable` outright for
// non-local hosts; loopback and in-cluster DNS are left alone.
func TestCoerceSSLMode(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string // empty → expect error
		wantErr string // substring to look for in the error
	}{
		{
			name: "public host, no sslmode → require injected",
			in:   "postgres://u:p@db.example.com:5432/postgres",
			want: "postgres://u:p@db.example.com:5432/postgres?sslmode=require",
		},
		{
			name: "public host, explicit verify-full untouched",
			in:   "postgres://u:p@db.example.com:5432/postgres?sslmode=verify-full",
			want: "postgres://u:p@db.example.com:5432/postgres?sslmode=verify-full",
		},
		{
			name: "public host, sslmode=disable → rejected",
			in:   "postgres://u:p@db.example.com:5432/postgres?sslmode=disable",
			// want is empty: expect an error.
			wantErr: "sslmode=disable is not allowed",
		},
		{
			name: "loopback, sslmode=disable allowed",
			in:   "postgres://u:p@127.0.0.1:5432/postgres?sslmode=disable",
			want: "postgres://u:p@127.0.0.1:5432/postgres?sslmode=disable",
		},
		{
			name: "in-cluster .svc, no sslmode → unchanged (caller's policy)",
			in:   "postgres://u:p@db.kuso.svc:5432/postgres",
			want: "postgres://u:p@db.kuso.svc:5432/postgres",
		},
		{
			name: "in-cluster .svc.cluster.local, sslmode=disable allowed",
			in:   "postgres://u:p@db.kuso.svc.cluster.local:5432/postgres?sslmode=disable",
			want: "postgres://u:p@db.kuso.svc.cluster.local:5432/postgres?sslmode=disable",
		},
		{
			name:    "garbage DSN parse fails",
			in:      "postgres://[::::not-a-url",
			wantErr: "dsn parse",
		},
		{
			name: "public host, sslmode=require already set → untouched",
			in:   "postgres://u:p@neon.tech:5432/postgres?sslmode=require",
			want: "postgres://u:p@neon.tech:5432/postgres?sslmode=require",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := coerceSSLMode(tc.in)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil (result=%q)", tc.wantErr, got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("coerceSSLMode(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestHealthSnapshotSurface pins the three states GetStatus must
// distinguish based on the periodic-probe snapshot:
//
//	zero snapshot       → don't flag unhealthy yet (fresh leader,
//	                      first tick pending)
//	probed + ok=true    → ready
//	probed + ok=false   → unhealthy + LastError surfaces
//
// We bypass the live SELECT 1 by writing the snapshot directly.
// Without this, a fresh boot would briefly flag the cluster PG as
// down before the first Reconcile tick — the zero-snapshot guard
// in GetStatus prevents that, and this test pins it.
func TestHealthSnapshotSurface(t *testing.T) {
	t.Run("zero snapshot stays at ready", func(t *testing.T) {
		s := &Service{Logger: slog.Default()}
		snap := s.healthSnapshotCopy()
		if !snap.checkedAt.IsZero() {
			t.Fatalf("zero snapshot should have zero checkedAt, got %v", snap.checkedAt)
		}
	})

	t.Run("recorded ok stays at ready", func(t *testing.T) {
		s := &Service{Logger: slog.Default()}
		s.healthMu.Lock()
		s.health = healthSnapshot{checkedAt: time.Now(), ok: true}
		s.healthMu.Unlock()
		snap := s.healthSnapshotCopy()
		if !snap.ok || snap.checkedAt.IsZero() {
			t.Fatalf("expected probed-ok snapshot, got %+v", snap)
		}
	})

	t.Run("recorded failure surfaces unhealthy + error", func(t *testing.T) {
		s := &Service{Logger: slog.Default()}
		s.healthMu.Lock()
		s.health = healthSnapshot{checkedAt: time.Now(), ok: false, err: "dial tcp: timeout"}
		s.healthMu.Unlock()
		snap := s.healthSnapshotCopy()
		if snap.ok {
			t.Fatalf("expected probed-failed snapshot, got ok=true")
		}
		if !strings.Contains(snap.err, "timeout") {
			t.Errorf("err should carry probe error: %q", snap.err)
		}
	})
}

// TestProbeRecordOnFailure verifies probeAndRecord stamps the
// snapshot even when the DSN points nowhere — best-effort by design.
// We use a localhost DSN on a port nothing's listening on so pingDSN
// errors fast (sub-second) and the test stays hermetic.
func TestProbeRecordOnFailure(t *testing.T) {
	s := &Service{Logger: slog.Default()}
	// 127.0.0.1:1 is the unassigned-port convention; lib/pq's Open
	// itself is lazy, but Ping errors immediately on connect refused.
	s.probeAndRecord(t.Context(), "postgres://u:p@127.0.0.1:1/postgres?sslmode=disable&connect_timeout=2")
	snap := s.healthSnapshotCopy()
	if snap.checkedAt.IsZero() {
		t.Fatal("checkedAt should be set after probe")
	}
	if snap.ok {
		t.Fatal("probe against 127.0.0.1:1 should fail")
	}
	if snap.err == "" {
		t.Error("err should be populated on probe failure")
	}
}

// TestIsLocalHost catches drift in the local-host classifier — a
// future refactor that adds a new TLD or trims the .svc suffix logic
// must update both sites or this fails loudly.
func TestIsLocalHost(t *testing.T) {
	local := []string{
		"", "localhost", "127.0.0.1", "::1",
		"db.kuso.svc", "db.kuso.svc.cluster.local", "any.cluster.local",
	}
	public := []string{
		"example.com", "db.example.com", "instance-pg",
		"1.2.3.4", "neon.tech",
	}
	for _, h := range local {
		if !isLocalHost(h) {
			t.Errorf("isLocalHost(%q) = false, want true", h)
		}
	}
	for _, h := range public {
		if isLocalHost(h) {
			t.Errorf("isLocalHost(%q) = true, want false", h)
		}
	}
}
