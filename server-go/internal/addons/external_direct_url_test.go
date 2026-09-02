package addons

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// In-cluster postgres publishes DIRECT_URL as the un-pooled, session-safe DSN
// so apps have one ${{ <addon>.DIRECT_URL }} contract for the work that cannot
// go through PgBouncer: migrations (golang-migrate takes a session advisory
// lock, which transaction pooling silently drops), CREATE INDEX CONCURRENTLY,
// and anything holding session state across transactions.
//
// External addons published no DIRECT_URL, so that contract broke exactly where
// it matters. A release hook written as `migrate -database "$DATABASE_URL"` —
// the shape kuso's own docs use — then runs against the pooler, where the
// advisory lock protects nothing and two replicas can migrate concurrently.
//
// The user's own DATABASE_URL already IS the direct endpoint (they gave us the
// provider's host), so mirror it when they haven't set DIRECT_URL themselves.
func TestMirrorExternalSecret_PublishesDirectURL(t *testing.T) {
	t.Parallel()
	s := fakeServiceWithSecrets(t, seedProj("tickero"))

	dsn := "postgres://u:pw@ext.example.com:5432/db?sslmode=require"
	if _, err := s.Add(context.Background(), "tickero", CreateAddonRequest{
		Name: "psdb",
		Kind: "postgres",
		ExternalCredentials: map[string]string{
			"DATABASE_URL":      dsn,
			"POSTGRES_USER":     "u",
			"POSTGRES_PASSWORD": "pw",
			"POSTGRES_DB":       "db",
		},
		Pooler: &kube.KusoAddonPooler{Enabled: true, ExternalBackend: true, Host: "ext.example.com"},
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	conn, err := s.Kube.Clientset.CoreV1().Secrets("kuso").
		Get(context.Background(), "tickero-psdb-conn", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("conn secret: %v", err)
	}

	got := string(conn.Data["DIRECT_URL"])
	if got == "" {
		t.Fatal("DIRECT_URL absent — ${{ psdb.DIRECT_URL }} can't resolve, so a " +
			"migration release hook has no un-pooled DSN to use and silently runs " +
			"through PgBouncer instead")
	}
	if got != dsn {
		t.Errorf("DIRECT_URL = %q, want the direct endpoint %q", got, dsn)
	}
	// It must NOT be the pooled URL — that's the whole distinction.
	if got == string(conn.Data["POOLER_URL"]) {
		t.Error("DIRECT_URL is the pooled URL; session-scoped work would break")
	}
}

// A user who supplies their own DIRECT_URL (a provider with a separate
// unpooled endpoint, which is common) keeps it.
func TestMirrorExternalSecret_KeepsSuppliedDirectURL(t *testing.T) {
	t.Parallel()
	s := fakeServiceWithSecrets(t, seedProj("tickero"))

	explicit := "postgres://u:pw@direct.ext.example.com:5432/db?sslmode=require"
	if _, err := s.Add(context.Background(), "tickero", CreateAddonRequest{
		Name: "psdb2",
		Kind: "postgres",
		ExternalCredentials: map[string]string{
			"DATABASE_URL": "postgres://u:pw@pooled.ext.example.com:5432/db",
			"DIRECT_URL":   explicit,
		},
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	conn, err := s.Kube.Clientset.CoreV1().Secrets("kuso").
		Get(context.Background(), "tickero-psdb2-conn", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("conn secret: %v", err)
	}
	if got := string(conn.Data["DIRECT_URL"]); got != explicit {
		t.Errorf("DIRECT_URL = %q, want the user's own %q", got, explicit)
	}
}
