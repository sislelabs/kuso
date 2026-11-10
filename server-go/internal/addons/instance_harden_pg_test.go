package addons

import (
	"context"
	"database/sql"
	"fmt"
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

	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000)
	a, b, reader, unstamped := "hd_a_"+suffix, "hd_b_"+suffix, "hd_reader_"+suffix, "hd_other_"+suffix
	exec := func(q string) {
		t.Helper()
		if _, err := admin.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
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
		for _, d := range []string{"postgres", "template1", adminDB} {
			_, _ = admin.ExecContext(c, fmt.Sprintf(`GRANT CONNECT ON DATABASE %s TO PUBLIC`, pq.QuoteIdentifier(d)))
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
	if res.TenantLocked != 2 || res.LimitsSet != 2 || len(res.SharedLocked) != 3 {
		t.Fatalf("result = %+v, want 2 tenants locked, 2 limits, 3 shared", res)
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
	if !canConnect(mustUser(t, dsn), "postgres") {
		t.Error("admin lost CONNECT on postgres")
	}

	var limit int
	if err := admin.QueryRowContext(ctx, `SELECT rolconnlimit FROM pg_roles WHERE rolname = $1`, a).Scan(&limit); err != nil {
		t.Fatal(err)
	}
	if limit != instanceConnLimitFloor {
		t.Errorf("rolconnlimit = %d, want %d", limit, instanceConnLimitFloor)
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

func mustUser(t *testing.T, dsn string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	return u.User.Username()
}

func TestDefaultInstanceConnLimit(t *testing.T) {
	cases := []struct{ max, inUse, want int }{
		{100, 0, 50},
		{100, 30, 60}, // never below twice the live usage
		{500, 10, 250},
		{20, 0, 50},
	}
	for _, c := range cases {
		if got := defaultInstanceConnLimit(c.max, c.inUse); got != c.want {
			t.Errorf("defaultInstanceConnLimit(%d, %d) = %d, want %d", c.max, c.inUse, got, c.want)
		}
	}
}
