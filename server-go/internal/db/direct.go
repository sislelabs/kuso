package db

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strings"
	"time"
)

// The control plane normally reaches Postgres through kuso-pgbouncer in
// transaction mode. That breaks anything session-scoped: a session
// advisory lock (the migration lock), a session SET (statement_timeout=0
// before a long index build), and CREATE INDEX CONCURRENTLY. Those run
// on a small direct pool to the primary instead ("maintenance"), with
// no statement timeout. When no direct path is reachable they fall back
// to the pooled one, with transaction-scoped locking for migrations.

// directDSN returns the DSN of the primary behind a pooled DSN:
// KUSO_DB_DIRECT_DSN when set, else the bundled pooler's host swapped for
// kuso-postgres-rw:5432. "" when dsn does not point at the bundled pooler
// (it is then assumed to be direct already).
func directDSN(dsn string) string {
	if v := strings.TrimSpace(os.Getenv("KUSO_DB_DIRECT_DSN")); v != "" {
		return v
	}
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return ""
	}
	host, rest, _ := strings.Cut(u.Hostname(), ".")
	if host != "kuso-pgbouncer" {
		return ""
	}
	h := "kuso-postgres-rw"
	if rest != "" {
		h += "." + rest
	}
	u.Host = net.JoinHostPort(h, "5432")
	return u.String()
}

// isBundledPooler reports whether dsn points at kuso-pgbouncer.
func isBundledPooler(dsn string) bool {
	u, err := url.Parse(dsn)
	if err != nil {
		return false
	}
	host, _, _ := strings.Cut(u.Hostname(), ".")
	return host == "kuso-pgbouncer"
}

// withSessionOption adds a libpq "options" startup parameter. Only for a
// direct connection: PgBouncer rejects startup parameters it does not
// ignore.
func withSessionOption(dsn, opt string) string {
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return dsn + " options='" + opt + "'"
	}
	q := u.Query()
	if cur := q.Get("options"); cur != "" {
		opt = cur + " " + opt
	}
	q.Set("options", opt)
	u.RawQuery = q.Encode()
	return u.String()
}

// openMaintenance opens the direct pool. sessionSafe reports whether its
// connections are real sessions (direct to Postgres), so session advisory
// locks and session SETs mean something on them.
func openMaintenance(pooledDSN string, log *slog.Logger) (pool *sql.DB, sessionSafe bool) {
	target := directDSN(pooledDSN)
	pooled := isBundledPooler(pooledDSN) && os.Getenv("KUSO_DB_DIRECT_DSN") == ""
	if target == "" {
		// Not the bundled pooler: the configured DSN is the direct path,
		// as it has always been treated.
		target = pooledDSN
	}
	m, err := sql.Open("postgres", withUTCSession(withSessionOption(target,
		"-c statement_timeout=0 -c idle_in_transaction_session_timeout=0")))
	if err == nil {
		m.SetMaxOpenConns(2)
		m.SetMaxIdleConns(1)
		m.SetConnMaxIdleTime(5 * time.Minute)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = m.PingContext(ctx)
		cancel()
		if err == nil {
			return m, true
		}
		_ = m.Close()
	}
	if pooled {
		log.Warn("db: direct connection to the primary unavailable; maintenance work goes through the pooler with transaction-scoped locks",
			"err", err)
		return nil, false
	}
	// An external DSN that rejects the options parameter: use the main
	// pool as before.
	log.Warn("db: maintenance pool unavailable; using the main pool", "err", err)
	return nil, true
}

// Maintenance returns a DB for long-running background work (janitors,
// index builds, the LogLine partition migration): the direct pool with no
// statement timeout when one is available, else d itself.
func (d *DB) Maintenance() *DB {
	if d.maint == nil {
		return d
	}
	return &DB{DB: d.maint, tenancy: d.tenancy}
}

func (d *DB) closeMaintenance() {
	if d.maint != nil {
		_ = d.maint.Close()
	}
}

// Statement timeouts for the app role, applied as role defaults (ALTER ROLE
// … IN DATABASE … SET) because PgBouncer rejects them as startup
// parameters. New pooled backends pick them up; existing ones keep the old
// value until PgBouncer recycles them.
const (
	defaultStatementTimeout = 60 * time.Second
	defaultIdleTxTimeout    = 5 * time.Minute
)

func durationEnv(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	if v == "0" || strings.EqualFold(v, "off") {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return def
	}
	return d
}

// applyRoleTimeouts makes the role defaults match KUSO_DB_STATEMENT_TIMEOUT
// (default 60s) and KUSO_DB_IDLE_TX_TIMEOUT (default 5m); 0 resets one.
// Idempotent: only differing settings are written.
func (d *DB) applyRoleTimeouts(ctx context.Context) error {
	want := map[string]time.Duration{
		"statement_timeout":                   durationEnv("KUSO_DB_STATEMENT_TIMEOUT", defaultStatementTimeout),
		"idle_in_transaction_session_timeout": durationEnv("KUSO_DB_IDLE_TX_TIMEOUT", defaultIdleTxTimeout),
	}
	var dbName string
	var cfg []sql.NullString
	if err := d.DB.QueryRowContext(ctx, `SELECT current_database()`).Scan(&dbName); err != nil {
		return fmt.Errorf("db: role timeouts: %w", err)
	}
	rows, err := d.DB.QueryContext(ctx, `
		SELECT unnest(s.setconfig) FROM pg_db_role_setting s
		  JOIN pg_roles r ON r.oid = s.setrole
		  JOIN pg_database b ON b.oid = s.setdatabase
		 WHERE r.rolname = current_user AND b.datname = current_database()`)
	if err != nil {
		return fmt.Errorf("db: role timeouts: %w", err)
	}
	for rows.Next() {
		var c sql.NullString
		if err := rows.Scan(&c); err != nil {
			rows.Close()
			return err
		}
		cfg = append(cfg, c)
	}
	rows.Close()
	have := map[string]string{}
	for _, c := range cfg {
		if k, v, ok := strings.Cut(c.String, "="); ok {
			have[k] = v
		}
	}
	qdb := quoteIdentAny(dbName)
	for name, dur := range want {
		var stmt string
		switch {
		case dur == 0 && have[name] != "":
			stmt = fmt.Sprintf(`ALTER ROLE CURRENT_USER IN DATABASE %s RESET %s`, qdb, name)
		case dur > 0 && have[name] != fmt.Sprint(dur.Milliseconds()):
			stmt = fmt.Sprintf(`ALTER ROLE CURRENT_USER IN DATABASE %s SET %s = %d`, qdb, name, dur.Milliseconds())
		default:
			continue
		}
		if _, err := d.DB.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("db: set %s: %w", name, err)
		}
	}
	return nil
}

// quoteIdentAny double-quotes an identifier, doubling embedded quotes.
func quoteIdentAny(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
