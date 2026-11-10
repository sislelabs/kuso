package addons

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/lib/pq"

	"kuso/server/internal/kube"
)

// AnnotationInstanceConnLimit on an instance-shared addon CR pins its role's
// CONNECTION LIMIT on the shared server. Applied on the next resync.
const AnnotationInstanceConnLimit = "kuso.sislelabs.com/pg-connection-limit"

// instanceConnLimitFloor sits above what one tenant legitimately holds: a
// pooled tenant gets default_pool_size 25 + reserve 5 backends per (user, db)
// pair, plus a few for DIRECT_URL (migrations, workers).
const instanceConnLimitFloor = 50

func instanceConnLimitOverride(a *kube.KusoAddon) int {
	if a == nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(a.Annotations[AnnotationInstanceConnLimit]))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// defaultInstanceConnLimit is half the server's max_connections, never below
// the floor, and never below twice what the role holds right now: a limit is
// applied to live tenants, and it must not stop a running app from opening
// the connections it already depends on.
func defaultInstanceConnLimit(maxConns, inUse int) int {
	n := maxConns / 2
	if n < instanceConnLimitFloor {
		n = instanceConnLimitFloor
	}
	if inUse*2 > n {
		n = inUse * 2
	}
	return n
}

// applyInstanceRoleConnLimit sets the tenant role's CONNECTION LIMIT. With no
// override it only fills in a role that has none (-1), so a limit an admin
// tuned by hand survives resyncs. ALTER ROLE never terminates open sessions;
// it only refuses new ones beyond the limit. Superusers are exempt.
func applyInstanceRoleConnLimit(ctx context.Context, db *sql.DB, role string, override int) error {
	var cur, inUse, maxConns int
	err := db.QueryRowContext(ctx, `
		SELECT r.rolconnlimit,
		       (SELECT count(*) FROM pg_stat_activity a WHERE a.usename = r.rolname),
		       current_setting('max_connections')::int
		  FROM pg_roles r WHERE r.rolname = $1`, role).Scan(&cur, &inUse, &maxConns)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read connection limit of %s: %w", role, err)
	}
	want := override
	if want <= 0 {
		if cur >= 0 {
			return nil
		}
		want = defaultInstanceConnLimit(maxConns, inUse)
	}
	if cur == want {
		return nil
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf(`ALTER ROLE %s CONNECTION LIMIT %d`, pq.QuoteIdentifier(role), want)); err != nil {
		return fmt.Errorf("set connection limit of %s: %w", role, err)
	}
	return nil
}

// publicCanConnect is true while PUBLIC still holds CONNECT on the database
// (a NULL datacl is the default ACL, which grants it).
const publicCanConnectExpr = `(d.datacl IS NULL OR EXISTS (
	SELECT 1 FROM aclexplode(d.datacl) x WHERE x.grantee = 0 AND x.privilege_type = 'CONNECT'))`

// lockDatabaseConnect revokes PUBLIC's CONNECT on dbName. Before that it
// grants CONNECT explicitly to keep, to the admin itself, and to every
// non-superuser role connected to the database right now, so nothing that
// works today loses access. All in one transaction.
func lockDatabaseConnect(ctx context.Context, db *sql.DB, dbName string, keep ...string) (preserved []string, err error) {
	rows, err := db.QueryContext(ctx, `
		SELECT DISTINCT a.usename FROM pg_stat_activity a
		  JOIN pg_roles r ON r.rolname = a.usename
		 WHERE a.datname = $1 AND NOT r.rolsuper AND a.usename <> current_user`, dbName)
	if err != nil {
		return nil, fmt.Errorf("list sessions on %s: %w", dbName, err)
	}
	roles := append([]string{}, keep...)
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan session role: %w", err)
		}
		roles = append(roles, r)
		if !contains(keep, r) {
			preserved = append(preserved, r)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list sessions on %s: %w", dbName, err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	qdb := pq.QuoteIdentifier(dbName)
	stmts := []string{fmt.Sprintf(`GRANT CONNECT ON DATABASE %s TO CURRENT_USER`, qdb)}
	seen := map[string]bool{}
	for _, r := range roles {
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		stmts = append(stmts, fmt.Sprintf(`GRANT CONNECT ON DATABASE %s TO %s`, qdb, pq.QuoteIdentifier(r)))
	}
	stmts = append(stmts, fmt.Sprintf(`REVOKE CONNECT ON DATABASE %s FROM PUBLIC`, qdb))
	for _, st := range stmts {
		if _, err := tx.ExecContext(ctx, st); err != nil {
			return nil, fmt.Errorf("lock connect on %s: %w", dbName, err)
		}
	}
	return preserved, tx.Commit()
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func preservedWarning(dbName string, roles []string) string {
	return fmt.Sprintf("kept CONNECT on %s for role(s) %s: connected when PUBLIC access was revoked; revoke by hand if unintended",
		dbName, strings.Join(roles, ", "))
}

// InstanceHardenResult counts what HardenInstanceServer changed.
type InstanceHardenResult struct {
	LimitsSet    int
	TenantLocked int
	SharedLocked []string
	Warnings     []string
}

// HardenInstanceServer brings an instance Postgres's existing tenants up to
// what provisioning does today, idempotently:
//
//   - every kuso-stamped tenant role without a CONNECTION LIMIT gets the
//     derived default;
//   - every kuso-stamped tenant DB that PUBLIC can still CONNECT to (DBs
//     provisioned before the revoke existed) is locked to its own role;
//   - when lockShared, postgres, template1 and the admin's own database are
//     locked the same way, so tenant roles cannot open sessions there.
//
// Roles connected to a database at the time keep CONNECT on it. Per-object
// failures are collected as warnings, not returned, so one DB the admin
// cannot alter does not stop the rest.
func HardenInstanceServer(ctx context.Context, adminDSN string, lockShared bool) (InstanceHardenResult, error) {
	var res InstanceHardenResult
	db, err := sql.Open("postgres", adminDSN)
	if err != nil {
		return res, fmt.Errorf("open admin: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	type tenant struct {
		name          string
		publicConnect bool
		hasRole       bool
		connLimit     int
	}
	rows, err := db.QueryContext(ctx, `
		SELECT d.datname, `+publicCanConnectExpr+`, r.rolname IS NOT NULL, COALESCE(r.rolconnlimit, -1)
		  FROM pg_database d LEFT JOIN pg_roles r ON r.rolname = d.datname
		 WHERE NOT d.datistemplate AND shobj_description(d.oid, 'pg_database') LIKE $1`,
		instanceDBOwnerPrefix+"%")
	if err != nil {
		return res, fmt.Errorf("list tenant databases: %w", err)
	}
	var tenants []tenant
	for rows.Next() {
		var t tenant
		if err := rows.Scan(&t.name, &t.publicConnect, &t.hasRole, &t.connLimit); err != nil {
			rows.Close()
			return res, fmt.Errorf("scan tenant database: %w", err)
		}
		tenants = append(tenants, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, fmt.Errorf("list tenant databases: %w", err)
	}

	for _, t := range tenants {
		if t.hasRole && t.connLimit < 0 {
			if err := applyInstanceRoleConnLimit(ctx, db, t.name, 0); err != nil {
				res.Warnings = append(res.Warnings, err.Error())
			} else {
				res.LimitsSet++
			}
		}
		if !t.publicConnect {
			continue
		}
		var keep []string
		if t.hasRole {
			keep = append(keep, t.name)
		}
		kept, err := lockDatabaseConnect(ctx, db, t.name, keep...)
		if err != nil {
			res.Warnings = append(res.Warnings, err.Error())
			continue
		}
		if len(kept) > 0 {
			res.Warnings = append(res.Warnings, preservedWarning(t.name, kept))
		}
		res.TenantLocked++
	}

	if !lockShared {
		return res, nil
	}
	shared, err := db.QueryContext(ctx, `
		SELECT d.datname FROM pg_database d
		 WHERE d.datname IN ('postgres', 'template1', current_database()) AND `+publicCanConnectExpr)
	if err != nil {
		return res, fmt.Errorf("list shared databases: %w", err)
	}
	var names []string
	for shared.Next() {
		var n string
		if err := shared.Scan(&n); err != nil {
			shared.Close()
			return res, fmt.Errorf("scan shared database: %w", err)
		}
		names = append(names, n)
	}
	shared.Close()
	for _, n := range names {
		kept, err := lockDatabaseConnect(ctx, db, n)
		if err != nil {
			res.Warnings = append(res.Warnings, err.Error())
			continue
		}
		if len(kept) > 0 {
			res.Warnings = append(res.Warnings, preservedWarning(n, kept))
		}
		res.SharedLocked = append(res.SharedLocked, n)
	}
	return res, nil
}

// disableInstanceRoleLogin turns a retained tenant role into NOLOGIN, so a
// DSN copied out before the delete stops working while the data stays. Open
// sessions are left alone. A re-add re-asserts LOGIN and the password.
func (s *Service) disableInstanceRoleLogin(ctx context.Context, adminDSN, project, addonShort string) error {
	role := pgIdentifier(project, addonShort)
	db, err := sql.Open("postgres", adminDSN)
	if err != nil {
		return fmt.Errorf("open admin: %w", err)
	}
	defer db.Close()
	var dbOwner, roleOwner sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT shobj_description(oid, 'pg_database') FROM pg_database WHERE datname = $1`, role).Scan(&dbOwner); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check pg_database: %w", err)
	}
	err = db.QueryRowContext(ctx, `SELECT shobj_description(oid, 'pg_authid') FROM pg_roles WHERE rolname = $1`, role).Scan(&roleOwner)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check pg_roles: %w", err)
	}
	if err := s.checkInstanceDBOwner(ctx, role, project, addonShort, dbOwner.String, roleOwner.String); err != nil {
		return fmt.Errorf("refusing to disable login: %w", err)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf(`ALTER ROLE %s NOLOGIN`, pq.QuoteIdentifier(role))); err != nil {
		return fmt.Errorf("disable login of %s: %w", role, err)
	}
	return nil
}
