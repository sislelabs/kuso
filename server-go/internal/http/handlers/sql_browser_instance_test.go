package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"kuso/server/internal/kube"
)

// BUG 46: an instance-backed addon (spec.useInstanceAddon) is a database on
// the SHARED kuso-instance-pg server. There is no <addon>-0 pod to exec
// into, and the single global kuso_browser role must never be provisioned
// there: one role name GRANTed CONNECT on every tenant's database, with a
// password each tenant's browser rotates, is a cross-tenant leak. The conn
// Secret's own role (<project>_<addon>) is already a NOSUPERUSER login that
// owns only that database, so the browser connects as it.

func instanceAddon() *kube.KusoAddon {
	return &kube.KusoAddon{Spec: kube.KusoAddonSpec{Kind: "postgres", UseInstanceAddon: "pg"}}
}

func TestNeedsBrowserRole_InstanceAddonSkips(t *testing.T) {
	if needsBrowserRole(instanceAddon()) {
		t.Error("instance addons must not provision the shared kuso_browser role on the multi-tenant server")
	}
}

// The instance server is in-cluster plaintext, unlike a managed provider.
func TestBrowserSSLMode_InstanceAddon(t *testing.T) {
	if got := browserSSLMode(instanceAddon()); got != "disable" {
		t.Errorf("instance sslmode = %q, want disable", got)
	}
	a := instanceAddon()
	a.Spec.TLS = "require"
	if got := browserSSLMode(a); got != "require" {
		t.Errorf("instance tls=require sslmode = %q, want require", got)
	}
}

func makeInstanceAddon(t *testing.T, h *BackupsHandler) {
	t.Helper()
	res := h.Kube.Dynamic.Resource(kube.GVRAddons).Namespace(customNS)
	u, err := res.Get(context.Background(), "e2e-db", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedField(u.Object, "pg", "spec", "useInstanceAddon"); err != nil {
		t.Fatal(err)
	}
	if _, err := res.Update(context.Background(), u, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

// The instance server lives in the home namespace, not the project's. A
// legacy conn Secret carrying the bare Service name must resolve there.
// And the ?database= override must stay on the addon's own database:
// older instance DBs still grant CONNECT to PUBLIC (live: 11 of 12), so a
// tenant role could otherwise open a neighbour's database.
func TestPGTarget_InstanceAddon(t *testing.T) {
	h := customNSBackupsHandler(t, "postgres", map[string]string{
		"POSTGRES_HOST":     "kuso-instance-pg",
		"POSTGRES_USER":     "e2e_db",
		"POSTGRES_PASSWORD": "pw",
		"POSTGRES_DB":       "e2e_db",
	})
	makeInstanceAddon(t, h)
	ctx := context.Background()

	tgt, err := h.pgTarget(ctx, "e2e", "db", "")
	if err != nil {
		t.Fatalf("pgTarget: %v", err)
	}
	if want := "kuso-instance-pg.kuso.svc"; tgt.host != want {
		t.Errorf("instance dial host = %q, want %q (home namespace)", tgt.host, want)
	}
	if tgt.user != "e2e_db" || tgt.name != "e2e_db" {
		t.Errorf("user/db = %q/%q, want the addon's own role and database", tgt.user, tgt.name)
	}
	if _, err := h.pgTarget(ctx, "e2e", "db", "e2e_db"); err != nil {
		t.Errorf("own database override rejected: %v", err)
	}
	if _, err := h.pgTarget(ctx, "e2e", "db", "bukvite_db"); err == nil {
		t.Error("override onto another tenant's database on the shared server was allowed")
	}
}

// Every privilege clause must stay in the check. rolsuper can't be caught
// behaviourally (pg_has_role already reports a superuser as a member of
// every role), so pin the text as well as the behaviour below.
func TestPrivilegedRoleQuery_KeepsEveryClause(t *testing.T) {
	for _, clause := range []string{
		"r.rolsuper",
		"r.rolcreaterole",
		"pg_has_role(current_user, 'pg_execute_server_program', 'MEMBER')",
		"pg_has_role(current_user, 'pg_read_server_files', 'MEMBER')",
		"pg_has_role(current_user, 'pg_write_server_files', 'MEMBER')",
	} {
		if !strings.Contains(privilegedRoleQuery, clause) {
			t.Errorf("privilegedRoleQuery lost %q", clause)
		}
	}
}

// requireUnprivilegedRole is the fail-closed check for sessions that skip
// kuso_browser: if the conn role turns out to be a superuser (hand-edited
// Secret, admin DSN pasted in) the browser must refuse. Needs a real
// Postgres (KUSO_TEST_PG_DSN, superuser).
func TestRequireUnprivilegedRole(t *testing.T) {
	dsn := os.Getenv("KUSO_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("KUSO_TEST_PG_DSN not set")
	}
	ctx := context.Background()
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if err := requireUnprivilegedRole(ctx, admin); err == nil {
		t.Error("superuser session accepted")
	}

	for _, stmt := range []string{
		`DROP ROLE IF EXISTS kuso_t_plain`, `CREATE ROLE kuso_t_plain LOGIN PASSWORD 'pw'`,
		`DROP ROLE IF EXISTS kuso_t_prog`, `CREATE ROLE kuso_t_prog LOGIN PASSWORD 'pw' IN ROLE pg_execute_server_program`,
		`DROP ROLE IF EXISTS kuso_t_cr`, `CREATE ROLE kuso_t_cr LOGIN CREATEROLE PASSWORD 'pw'`,
		`DROP ROLE IF EXISTS kuso_t_read`, `CREATE ROLE kuso_t_read LOGIN PASSWORD 'pw' IN ROLE pg_read_server_files`,
		`DROP ROLE IF EXISTS kuso_t_write`, `CREATE ROLE kuso_t_write LOGIN PASSWORD 'pw' IN ROLE pg_write_server_files`,
	} {
		if _, err := admin.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	t.Cleanup(func() {
		for _, r := range []string{"kuso_t_plain", "kuso_t_prog", "kuso_t_cr", "kuso_t_read", "kuso_t_write"} {
			_, _ = admin.Exec(`DROP ROLE IF EXISTS ` + r)
		}
	})
	as := func(role string) *sql.DB {
		i := strings.Index(dsn, "://")
		at := strings.LastIndex(dsn, "@")
		d, err := sql.Open("postgres", fmt.Sprintf("%s://%s:pw%s", dsn[:i], role, dsn[at:]))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = d.Close() })
		return d
	}
	if err := requireUnprivilegedRole(ctx, as("kuso_t_plain")); err != nil {
		t.Errorf("plain role refused: %v", err)
	}
	if err := requireUnprivilegedRole(ctx, as("kuso_t_prog")); err == nil {
		t.Error("pg_execute_server_program member accepted")
	}
	if err := requireUnprivilegedRole(ctx, as("kuso_t_cr")); err == nil {
		t.Error("CREATEROLE role accepted")
	}
	if err := requireUnprivilegedRole(ctx, as("kuso_t_read")); err == nil {
		t.Error("pg_read_server_files member accepted")
	}
	if err := requireUnprivilegedRole(ctx, as("kuso_t_write")); err == nil {
		t.Error("pg_write_server_files member accepted")
	}
}
