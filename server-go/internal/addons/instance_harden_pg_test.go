package addons

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/lib/pq"
)

// Exercises the exact harden SQL against a real Postgres: tenant DBs and
// the shared DBs get PUBLIC CONNECT revoked, each tenant keeps its own DB,
// a role connected at heal time keeps access, and roles get the default
// CONNECTION LIMIT without an existing session being cut.
func TestHardenInstanceServer_PG(t *testing.T) {
	dsn := os.Getenv("KUSO_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("KUSO_TEST_PG_DSN not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	var adminDB string
	if err := admin.QueryRowContext(ctx, `SELECT current_database()`).Scan(&adminDB); err != nil {
		t.Fatal(err)
	}

	// HardenInstanceServer acts on the whole server (roles and database ACLs
	// are cluster-wide): concurrent runs against one server take turns, and
	// kuso-stamped tenants other tests left behind are counted in and put
	// back afterwards.
	lockConn, err := admin.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockConn.ExecContext(ctx, `SELECT pg_advisory_lock(hashtext('kuso-test-harden-instance'))`); err != nil {
		t.Fatal(err)
	}
	// Registered first so it runs last: the lock covers the restore below.
	t.Cleanup(func() { _ = lockConn.Close() })

	suffix := uniqueProject(t)
	a, b, reader, unstamped := "hd_a_"+suffix, "hd_b_"+suffix, "hd_reader_"+suffix, "hd_other_"+suffix
	exec := func(q string) {
		t.Helper()
		if _, err := admin.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	names := func(q string) []string {
		t.Helper()
		rows, err := admin.QueryContext(ctx, q)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				t.Fatal(err)
			}
			out = append(out, n)
		}
		return out
	}
	shared := []string{"postgres", "template1", adminDB}
	// A run killed before its cleanup leaves these locked.
	for _, d := range shared {
		exec(fmt.Sprintf(`GRANT CONNECT ON DATABASE %s TO PUBLIC`, pq.QuoteIdentifier(d)))
	}
	foreignOpen := names(`SELECT datname FROM pg_database
		 WHERE NOT datistemplate AND shobj_description(oid, 'pg_database') LIKE 'kuso-addon:%'
		   AND has_database_privilege('public', oid, 'CONNECT')`)
	foreignUnlimited := names(`SELECT r.rolname FROM pg_database d JOIN pg_roles r ON r.rolname = d.datname
		 WHERE NOT d.datistemplate AND shobj_description(d.oid, 'pg_database') LIKE 'kuso-addon:%'
		   AND r.rolconnlimit = -1`)
	for _, r := range []string{a, b, reader} {
		exec(fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD 'pw'`, pq.QuoteIdentifier(r)))
	}
	for _, d := range []string{a, b, unstamped} {
		exec(fmt.Sprintf(`CREATE DATABASE %s`, pq.QuoteIdentifier(d)))
	}
	exec(fmt.Sprintf(`COMMENT ON DATABASE %s IS 'kuso-addon:p/a'`, pq.QuoteIdentifier(a)))
	exec(fmt.Sprintf(`COMMENT ON DATABASE %s IS 'kuso-addon:p/b'`, pq.QuoteIdentifier(b)))
	t.Cleanup(func() {
		c := context.Background()
		for _, d := range append(shared, foreignOpen...) {
			_, _ = admin.ExecContext(c, fmt.Sprintf(`GRANT CONNECT ON DATABASE %s TO PUBLIC`, pq.QuoteIdentifier(d)))
		}
		for _, r := range foreignUnlimited {
			_, _ = admin.ExecContext(c, fmt.Sprintf(`ALTER ROLE %s CONNECTION LIMIT -1`, pq.QuoteIdentifier(r)))
		}
		for _, d := range []string{a, b, unstamped} {
			_, _ = admin.ExecContext(c, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1`, d)
			_, _ = admin.ExecContext(c, fmt.Sprintf(`DROP DATABASE IF EXISTS %s`, pq.QuoteIdentifier(d)))
		}
		for _, r := range []string{a, b, reader} {
			_, _ = admin.ExecContext(c, fmt.Sprintf(`DROP ROLE IF EXISTS %s`, pq.QuoteIdentifier(r)))
		}
		_ = admin.Close()
	})

	as := func(role, db string) *sql.DB {
		u, _ := url.Parse(dsn)
		u.User = url.UserPassword(role, "pw")
		u.Path = "/" + db
		c, _ := sql.Open("postgres", u.String())
		c.SetMaxOpenConns(1)
		return c
	}
	canConnect := func(role, db string) bool {
		c := as(role, db)
		defer c.Close()
		return c.PingContext(ctx) == nil
	}

	// A legacy consumer holds a session on tenant a while the heal runs,
	// and tenant a's own role holds one too (it must survive the limit).
	held := as(reader, a)
	defer held.Close()
	if err := held.PingContext(ctx); err != nil {
		t.Fatalf("reader session: %v", err)
	}
	own := as(a, a)
	defer own.Close()
	if err := own.PingContext(ctx); err != nil {
		t.Fatalf("tenant session: %v", err)
	}

	res, err := HardenInstanceServer(ctx, dsn, true)
	if err != nil {
		t.Fatalf("harden: %v", err)
	}
	wantShared, gotShared := map[string]bool{}, map[string]bool{}
	for _, d := range shared {
		wantShared[d] = true
	}
	for _, d := range res.SharedLocked {
		gotShared[d] = true
	}
	if res.TenantLocked != 2+len(foreignOpen) || res.LimitsSet != 2+len(foreignUnlimited) ||
		len(res.SharedLocked) != len(wantShared) || !maps.Equal(gotShared, wantShared) {
		t.Fatalf("result = %+v, want %d tenants locked, %d limits, shared %v (other kuso tenants: open %v, unlimited %v)",
			res, 2+len(foreignOpen), 2+len(foreignUnlimited), shared, foreignOpen, foreignUnlimited)
	}
	if err := own.PingContext(ctx); err != nil {
		t.Fatalf("open tenant session cut by the heal: %v", err)
	}

	checks := []struct {
		role, db string
		want     bool
	}{
		{a, a, true},
		{b, b, true},
		{b, a, false},
		{a, b, false},
		{reader, a, true}, // connected at heal time: kept
		{reader, b, false},
		{a, "postgres", false},
		{a, adminDB, false},
		{a, unstamped, true}, // not kuso's: untouched
	}
	for _, c := range checks {
		if got := canConnect(c.role, c.db); got != c.want {
			t.Errorf("%s -> %s: connect=%v, want %v", c.role, c.db, got, c.want)
		}
	}
	adminURL, _ := url.Parse(dsn)
	adminURL.Path = "/postgres"
	adminPG, _ := sql.Open("postgres", adminURL.String())
	if err := adminPG.PingContext(ctx); err != nil {
		t.Errorf("admin lost CONNECT on postgres: %v", err)
	}
	_ = adminPG.Close()

	var limit int
	if err := admin.QueryRowContext(ctx, `SELECT rolconnlimit FROM pg_roles WHERE rolname = $1`, a).Scan(&limit); err != nil {
		t.Fatal(err)
	}
	var maxConns, reserved, stamped int
	if err := admin.QueryRowContext(ctx, `
		SELECT current_setting('max_connections')::int,
		       current_setting('superuser_reserved_connections')::int
		         + COALESCE(NULLIF(current_setting('reserved_connections', true), ''), '0')::int,
		       (SELECT count(*) FROM pg_roles WHERE rolcanlogin AND shobj_description(oid, 'pg_authid') LIKE 'kuso-addon:%')`).
		Scan(&maxConns, &reserved, &stamped); err != nil {
		t.Fatal(err)
	}
	// Unstamped roles are not counted as tenants, so on a clean server a is
	// the lone tenant; stamped roles other tests left behind split it.
	want := max(maxConns/2, instanceConnLimitFloor)
	if stamped > 1 {
		want = defaultInstanceConnLimit(maxConns, reserved, stamped, 1)
	}
	if limit != want {
		t.Errorf("rolconnlimit = %d, want %d (%d stamped tenant roles)", limit, want, stamped)
	}

	// Idempotent, and an admin-tuned limit survives.
	exec(fmt.Sprintf(`ALTER ROLE %s CONNECTION LIMIT 7`, pq.QuoteIdentifier(b)))
	res, err = HardenInstanceServer(ctx, dsn, true)
	if err != nil {
		t.Fatalf("second harden: %v", err)
	}
	if res.TenantLocked != 0 || res.LimitsSet != 0 || len(res.SharedLocked) != 0 {
		t.Fatalf("second pass changed things: %+v", res)
	}
	if err := admin.QueryRowContext(ctx, `SELECT rolconnlimit FROM pg_roles WHERE rolname = $1`, b).Scan(&limit); err != nil || limit != 7 {
		t.Errorf("admin-tuned limit = %d (%v), want 7", limit, err)
	}

	// An explicit override wins on resync.
	if err := applyInstanceRoleConnLimit(ctx, admin, b, 12); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRowContext(ctx, `SELECT rolconnlimit FROM pg_roles WHERE rolname = $1`, b).Scan(&limit); err != nil || limit != 12 {
		t.Errorf("override limit = %d (%v), want 12", limit, err)
	}
}

// The default splits the server across the kuso-stamped tenant roles on it.
func TestApplyInstanceRoleConnLimit_SplitsAcrossTenants_PG(t *testing.T) {
	dsn := os.Getenv("KUSO_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("KUSO_TEST_PG_DSN not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000)
	roles := []string{"cl_a_" + suffix, "cl_b_" + suffix}
	t.Cleanup(func() {
		for _, r := range roles {
			_, _ = admin.ExecContext(context.Background(), fmt.Sprintf(`DROP ROLE IF EXISTS %s`, pq.QuoteIdentifier(r)))
		}
		_ = admin.Close()
	})
	for _, r := range roles {
		for _, q := range []string{
			fmt.Sprintf(`CREATE ROLE %s LOGIN`, pq.QuoteIdentifier(r)),
			fmt.Sprintf(`COMMENT ON ROLE %s IS 'kuso-addon:p/%s'`, pq.QuoteIdentifier(r), r),
		} {
			if _, err := admin.ExecContext(ctx, q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
	}
	var maxConns, reserved, tenants int
	if err := admin.QueryRowContext(ctx, `
		SELECT current_setting('max_connections')::int,
		       current_setting('superuser_reserved_connections')::int
		         + COALESCE(NULLIF(current_setting('reserved_connections', true), ''), '0')::int,
		       (SELECT count(*) FROM pg_roles t WHERE t.rolcanlogin AND shobj_description(t.oid, 'pg_authid') LIKE 'kuso-addon:%')`).
		Scan(&maxConns, &reserved, &tenants); err != nil {
		t.Fatal(err)
	}
	want := (maxConns - reserved - instanceConnHeadroom) / tenants
	if want >= maxConns/2 || want < instanceConnLimitFloor {
		t.Skipf("server shape (max_connections=%d, %d tenants) does not exercise the split", maxConns, tenants)
	}
	if err := applyInstanceRoleConnLimit(ctx, admin, roles[0], 0); err != nil {
		t.Fatal(err)
	}
	var got int
	if err := admin.QueryRowContext(ctx, `SELECT rolconnlimit FROM pg_roles WHERE rolname = $1`, roles[0]).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("rolconnlimit with %d tenants on max_connections=%d = %d, want %d", tenants, maxConns, got, want)
	}
}

func TestDefaultInstanceConnLimit(t *testing.T) {
	cases := []struct{ max, reserved, tenants, inUse, want int }{
		{100, 3, 1, 0, 50},   // lone tenant: half the server
		{100, 3, 2, 0, 46},   // (100-3-5)/2
		{100, 3, 10, 0, 35},  // floor: a pooled tenant's own backends
		{100, 3, 10, 30, 60}, // never below twice the live usage
		{500, 3, 4, 0, 123},
		{500, 3, 0, 0, 250},
		{20, 3, 1, 0, 35},
	}
	for _, c := range cases {
		if got := defaultInstanceConnLimit(c.max, c.reserved, c.tenants, c.inUse); got != c.want {
			t.Errorf("defaultInstanceConnLimit(%+v) = %d", c, got)
		}
	}
}

func TestWithConnectTimeout(t *testing.T) {
	cases := map[string]string{
		"postgres://u:p@h:5432/db?sslmode=disable":    "postgres://u:p@h:5432/db?connect_timeout=5&sslmode=disable",
		"postgres://u:p@h:5432/db?connect_timeout=30": "postgres://u:p@h:5432/db?connect_timeout=30",
		"host=h user=u dbname=db":                     "host=h user=u dbname=db",
	}
	for in, want := range cases {
		if got := withConnectTimeout(in); got != want {
			t.Errorf("withConnectTimeout(%q) = %q, want %q", in, got, want)
		}
	}
}
