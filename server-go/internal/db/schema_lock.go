package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"
)

// A schema change queued behind a long transaction blocks every later
// reader of that table until it gets its lock, so DDL waits at most
// schemaLockTimeout and is retried rather than parked indefinitely.
// Vars so tests can shrink them.
var (
	schemaLockTimeout    = 3 * time.Second
	schemaLockAttempts   = 6
	schemaLockBackoff    = time.Second
	schemaLockMaxBackoff = 15 * time.Second
)

func isLockTimeout(err error) bool {
	var pqe *pq.Error
	return errors.As(err, &pqe) && pqe.Code == "55P03"
}

// retryOnLockTimeout runs fn until it succeeds, fails with something
// other than a lock timeout, or schemaLockAttempts is exhausted.
func retryOnLockTimeout(ctx context.Context, fn func() error) error {
	delay := schemaLockBackoff
	var err error
	for attempt := 1; attempt <= schemaLockAttempts; attempt++ {
		if err = fn(); err == nil || !isLockTimeout(err) {
			return err
		}
		if attempt == schemaLockAttempts {
			break
		}
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return err
		case <-t.C:
		}
		if delay *= 2; delay > schemaLockMaxBackoff {
			delay = schemaLockMaxBackoff
		}
	}
	return fmt.Errorf("gave up after %d lock-timeout attempts: %w", schemaLockAttempts, err)
}

// setLocalLockTimeout scopes lock_timeout to tx. Transaction-local, so it
// is safe behind the transaction-mode pooler.
func setLocalLockTimeout(ctx context.Context, tx *sql.Tx) error {
	ms := strconv.FormatInt(schemaLockTimeout.Milliseconds(), 10) + "ms"
	_, err := tx.ExecContext(ctx, `SELECT set_config('lock_timeout', $1, true)`, ms)
	return err
}

// execDDL runs one baseline statement in its own transaction under
// lock_timeout, retrying lock timeouts.
func execDDL(ctx context.Context, pool *sql.DB, stmt string) error {
	return retryOnLockTimeout(ctx, func() error {
		tx, err := pool.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		if err := setLocalLockTimeout(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
		return tx.Commit()
	})
}

var (
	alterTableHeadRe = regexp.MustCompile(`(?is)^ALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:ONLY\s+)?("(?:[^"]|"")+"|[A-Za-z_][A-Za-z0-9_$]*)\s+(.*)$`)
	addColumnIfNERe  = regexp.MustCompile(`(?is)^ADD\s+(?:COLUMN\s+)?IF\s+NOT\s+EXISTS\s+("(?:[^"]|"")+"|[A-Za-z_][A-Za-z0-9_$]*)(?:\s|$)`)
)

// pgIdent returns the catalog spelling of an identifier: quoted names
// keep their case, bare names fold to lower case.
func pgIdent(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return strings.ReplaceAll(s[1:len(s)-1], `""`, `"`)
	}
	return strings.ToLower(s)
}

// splitTopLevelCommas splits on commas outside parentheses and quotes,
// so `DEFAULT '[]'` and `NUMERIC(10,2)` stay inside their clause.
func splitTopLevelCommas(s string) []string {
	var out []string
	depth, start := 0, 0
	inSingle, inDouble := false, false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\'' && !inDouble:
			inSingle = !inSingle
		case c == '"' && !inSingle:
			inDouble = !inDouble
		case inSingle || inDouble:
		case c == '(':
			depth++
		case c == ')':
			depth--
		case c == ',' && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// parseAddColumnsIfNotExists recognises a statement made only of
// `ADD [COLUMN] IF NOT EXISTS` clauses and returns the table and the
// columns it adds. ok is false for anything else.
func parseAddColumnsIfNotExists(stmt string) (table string, cols []string, ok bool) {
	m := alterTableHeadRe.FindStringSubmatch(strings.TrimSpace(stmt))
	if m == nil {
		return "", nil, false
	}
	for _, clause := range splitTopLevelCommas(m[2]) {
		cm := addColumnIfNERe.FindStringSubmatch(strings.TrimSpace(clause))
		if cm == nil {
			return "", nil, false
		}
		cols = append(cols, pgIdent(cm[1]))
	}
	return pgIdent(m[1]), cols, len(cols) > 0
}

// columnsAllExist reports whether every column is already on table in
// the current schema. A catalog read takes no table lock, unlike
// ADD COLUMN IF NOT EXISTS, which waits for ACCESS EXCLUSIVE before it
// checks.
func columnsAllExist(ctx context.Context, pool *sql.DB, table string, cols []string) (bool, error) {
	var n int
	err := pool.QueryRowContext(ctx,
		`SELECT count(DISTINCT column_name) FROM information_schema.columns
		 WHERE table_schema = current_schema() AND table_name = $1 AND column_name = ANY($2)`,
		table, pq.StringArray(cols)).Scan(&n)
	if err != nil {
		return false, err
	}
	return n == len(cols), nil
}
