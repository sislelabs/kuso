// Package audit owns the Audit table writes + reads.
//
// Audit is ON by default — a platform doing destructive ops needs a
// trail unless the operator explicitly opts out via KUSO_AUDIT=false.
// When disabled, every method is a silent no-op so handler call sites
// don't need a guard.
package audit

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"time"

	"kuso/server/internal/db"
)

// Service is the entrypoint for write+read of audit entries. Construct
// with New; safe for concurrent use. New starts one trim goroutine
// per Service — call sites that re-construct (tests) should pass a
// derivable ctx so the loop unwinds with the caller's lifetime.
//
// Retention is by age, not count: a pure row cap let anyone able to
// write cheap rows (sql:read runs one audited query per call) evict
// earlier evidence within one trim tick. Security-relevant rows
// (warn/critical/error) are only ever pruned by SecurityMaxAge; the
// MaxBackups count cap is a disk-safety valve over routine rows only.
type Service struct {
	DB      *db.DB
	Enabled bool
	// MaxBackups caps the number of ROUTINE (info/normal) rows kept.
	MaxBackups int
	// MaxAge prunes routine rows older than this. 0 = no age prune.
	MaxAge time.Duration
	// SecurityMaxAge prunes warn/critical/error rows older than this.
	// 0 = keep forever.
	SecurityMaxAge time.Duration

	mu sync.Mutex // guards the periodic trim() call
}

const (
	defaultMaxRoutineRows   = 200000
	defaultRetentionDays    = 90
	defaultSecRetentionDays = 365
	day                     = 24 * time.Hour
)

// securitySeverities never fall to the count cap. Kept as a SQL literal
// list because it is a fixed constant, never user input.
const securitySeverities = `('warn','critical','error')`

// IsSecuritySeverity reports whether a row of this severity is exempt
// from the routine-row count cap.
func IsSecuritySeverity(sev string) bool {
	switch sev {
	case "warn", "critical", "error":
		return true
	}
	return false
}

// New constructs a Service. Enabled by default; KUSO_AUDIT=false opts
// out. Retention knobs:
//   - KUSO_AUDIT_RETENTION_DAYS (default 90): routine-row age limit.
//   - KUSO_AUDIT_SECURITY_RETENTION_DAYS (default 365, 0 = forever):
//     warn/critical/error age limit.
//   - KUSO_AUDIT_LIMIT (default 200000): count cap on routine rows only.
//
// Unparseable or negative values fall back to the default (fail safe,
// never uncap by typo). KUSO_AUDIT_TRIM_TIMEOUT overrides the per-tick
// context bound (default 60s).
//
// When enabled, New also starts the singleton trim ticker bound to
// ctx. Pass context.Background() in tests where you don't need
// shutdown semantics.
func New(ctx context.Context, d *db.DB) *Service {
	enabled := os.Getenv("KUSO_AUDIT") != "false"
	limit := defaultMaxRoutineRows
	if v := os.Getenv("KUSO_AUDIT_LIMIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	trimTimeout := 60 * time.Second
	if v := os.Getenv("KUSO_AUDIT_TRIM_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			trimTimeout = d
		}
	}
	s := &Service{
		DB:             d,
		Enabled:        enabled,
		MaxBackups:     limit,
		MaxAge:         envDays("KUSO_AUDIT_RETENTION_DAYS", defaultRetentionDays, false),
		SecurityMaxAge: envDays("KUSO_AUDIT_SECURITY_RETENTION_DAYS", defaultSecRetentionDays, true),
	}
	if enabled && d != nil {
		go s.runTrimLoop(ctx, trimTimeout)
	}
	return s
}

// envDays reads a whole-day count (time.ParseDuration has no day unit).
// allowZero lets 0 mean "keep forever".
func envDays(key string, def int, allowZero bool) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return time.Duration(def) * day
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || (n == 0 && !allowZero) {
		return time.Duration(def) * day
	}
	return time.Duration(n) * day
}

// runTrimLoop fires every 5 minutes until ctx is done. Errors log
// instead of swallowing — silent failure was the cause of the
// SQLite-only-LIMIT-syntax regression that grew Audit unbounded.
func (s *Service) runTrimLoop(ctx context.Context, perTickTimeout time.Duration) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tickCtx, cancel := context.WithTimeout(ctx, perTickTimeout)
			if err := s.trim(tickCtx); err != nil {
				slog.Error("audit: trim failed", "err", err)
			}
			cancel()
		}
	}
}

// Entry is one audit record. JSON tags pin the wire shape — the
// UI table renders these and tests assert on them. Empty fields
// round-trip as "" rather than absent because the audit row schema
// stores NOT NULL on every column, so the consumer never has to
// branch on undefined.
type Entry struct {
	ID        int64     `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	User      string    `json:"user"`
	Severity  string    `json:"severity"`
	Action    string    `json:"action"`
	Namespace string    `json:"namespace"`
	Phase     string    `json:"phase"`
	App       string    `json:"app"`
	Pipeline  string    `json:"pipeline"`
	Resource  string    `json:"resource"`
	Message   string    `json:"message"`
}

// Log writes one entry. No-op when disabled or DB is nil.
func (s *Service) Log(ctx context.Context, e Entry) {
	if s == nil || !s.Enabled || s.DB == nil {
		return
	}
	if e.User == "" {
		e.User = "1" // system user
	}
	if e.Severity == "" {
		e.Severity = "normal"
	}
	if e.Resource == "" {
		e.Resource = "unknown"
	}
	now := time.Now().UTC()
	// `user` is a Postgres reserved word; camelCase columns also
	// need quoting. Unquoted, the INSERT silently fails the FK.
	if _, err := s.DB.ExecContext(ctx, `
INSERT INTO "Audit" (timestamp, severity, action, namespace, phase, app, pipeline, resource, message, "user", "createdAt", "updatedAt")
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		now, e.Severity, e.Action, e.Namespace, e.Phase, e.App, e.Pipeline, e.Resource, e.Message, e.User, now, now,
	); err != nil {
		// Logging an audit row must never affect the call site — log
		// the failure and move on.
		slog.Error("audit: log failed", "err", err)
		return
	}
	// Mirror security-relevant rows to the server log too: pod stdout is
	// collected by whatever ships cluster logs off-box, so these survive a
	// control-plane DB restore or a compromised Postgres.
	if IsSecuritySeverity(e.Severity) {
		slog.Info("audit", "severity", e.Severity, "action", e.Action, "user", e.User,
			"project", e.Pipeline, "app", e.App, "resource", e.Resource, "message", e.Message)
	}
	// Trim runs on the singleton ticker started by New.
}

// Get returns the newest `limit` rows. Pagination is keyset on id:
// pass after=<id> to fetch the page older than that id (0 = newest
// page). `more` reports whether older rows exist beyond the returned
// page — determined by over-fetching one row, so it's exact, not a
// full-page heuristic.
func (s *Service) Get(ctx context.Context, after int64, limit int) (entries []Entry, total int, more bool, err error) {
	if s == nil || !s.Enabled {
		return nil, 0, false, nil
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	q := `
SELECT id, timestamp, severity, action, namespace, phase, app, pipeline, resource, message, "user"
FROM "Audit"`
	args := []any{}
	if after > 0 {
		args = append(args, after)
		q += fmt.Sprintf(" WHERE id < $%d", len(args))
	}
	args = append(args, limit+1) // +1 row detects truncation
	q += fmt.Sprintf(" ORDER BY id DESC LIMIT $%d", len(args))
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, false, fmt.Errorf("audit: get: %w", err)
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.Timestamp, &e.Severity, &e.Action, &e.Namespace, &e.Phase, &e.App, &e.Pipeline, &e.Resource, &e.Message, &e.User); err != nil {
			return nil, 0, false, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, false, err
	}
	if len(out) > limit {
		more = true
		out = out[:limit]
	}
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM "Audit"`).Scan(&total)
	return out, total, more, nil
}

// GetForProject returns audit rows filtered by project. The Audit
// table's `pipeline` column carries v0.1's pipeline name and v0.2's
// project name; both share the lifetime "this is the top-level
// container" semantics, so a single column is fine.
//
// Pagination is keyset on id: pass after=<id> to fetch the page
// older than that id. limit is clamped [1, 1000]. `more` reports
// whether older rows exist beyond the returned page (exact —
// determined by over-fetching one row).
func (s *Service) GetForProject(ctx context.Context, project string, after int64, limit int) (entries []Entry, total int, more bool, err error) {
	if s == nil || !s.Enabled {
		return nil, 0, false, nil
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	// Postgres positional params: number each placeholder by its arg
	// position as the slice grows (the optional `after` clause shifts the
	// LIMIT placeholder, so we can't hard-code the indices).
	q := `
SELECT id, timestamp, severity, action, namespace, phase, app, pipeline, resource, message, "user"
FROM "Audit" WHERE pipeline = $1`
	args := []any{project}
	if after > 0 {
		args = append(args, after)
		q += fmt.Sprintf(" AND id < $%d", len(args))
	}
	args = append(args, limit+1) // +1 row detects truncation
	q += fmt.Sprintf(" ORDER BY id DESC LIMIT $%d", len(args))

	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, false, fmt.Errorf("audit: get for project: %w", err)
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.Timestamp, &e.Severity, &e.Action, &e.Namespace, &e.Phase, &e.App, &e.Pipeline, &e.Resource, &e.Message, &e.User); err != nil {
			return nil, 0, false, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, false, err
	}
	if len(out) > limit {
		more = true
		out = out[:limit]
	}
	_ = s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM "Audit" WHERE pipeline = $1`,
		project).Scan(&total)
	return out, total, more, nil
}

// GetForApp returns the newest rows filtered by pipeline+phase+app.
// phase "*" matches every env: most actions record no phase at all.
// `more` reports whether older rows exist beyond the returned page
// (exact — determined by over-fetching one row).
// Rows older than since are skipped (zero = all).
func (s *Service) GetForApp(ctx context.Context, pipeline, phase, app string, since time.Time, limit int) (entries []Entry, total int, more bool, err error) {
	if s == nil || !s.Enabled {
		return nil, 0, false, nil
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.DB.QueryContext(ctx, `
SELECT id, timestamp, severity, action, namespace, phase, app, pipeline, resource, message, "user"
FROM "Audit" WHERE pipeline = $1 AND ($2 = '*' OR phase = $2) AND app = $3 AND timestamp >= $5
ORDER BY id DESC LIMIT $4`, pipeline, phase, app, limit+1, since.UTC()) // +1 row detects truncation
	if err != nil {
		return nil, 0, false, fmt.Errorf("audit: get app: %w", err)
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.Timestamp, &e.Severity, &e.Action, &e.Namespace, &e.Phase, &e.App, &e.Pipeline, &e.Resource, &e.Message, &e.User); err != nil {
			return nil, 0, false, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, false, err
	}
	if len(out) > limit {
		more = true
		out = out[:limit]
	}
	_ = s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM "Audit" WHERE pipeline = $1 AND ($2 = '*' OR phase = $2) AND app = $3 AND timestamp >= $4`,
		pipeline, phase, app, since.UTC()).Scan(&total)
	return out, total, more, nil
}

// trim applies the retention policy: routine rows past MaxAge, security
// rows past SecurityMaxAge, then routine rows beyond the MaxBackups
// newest. Returns nil when another trim is already in flight (TryLock
// miss). Errors propagate so the caller can log — silent failure was how
// the SQLite-only LIMIT syntax let the table grow unbounded.
func (s *Service) trim(ctx context.Context) error {
	if !s.mu.TryLock() {
		return nil
	}
	defer s.mu.Unlock()
	now := time.Now().UTC()
	if s.MaxAge > 0 {
		if _, err := s.DB.ExecContext(ctx,
			`DELETE FROM "Audit" WHERE "timestamp" < $1 AND severity NOT IN `+securitySeverities,
			now.Add(-s.MaxAge)); err != nil {
			return fmt.Errorf("audit: trim routine by age: %w", err)
		}
	}
	if s.SecurityMaxAge > 0 {
		if _, err := s.DB.ExecContext(ctx,
			`DELETE FROM "Audit" WHERE "timestamp" < $1 AND severity IN `+securitySeverities,
			now.Add(-s.SecurityMaxAge)); err != nil {
			return fmt.Errorf("audit: trim security by age: %w", err)
		}
	}
	if s.MaxBackups > 0 {
		// The OFFSET row is the (MaxBackups+1)-th newest routine row;
		// deleting it and every older routine row keeps exactly
		// MaxBackups. No row at that offset → NULL → nothing deleted.
		if _, err := s.DB.ExecContext(ctx, `
DELETE FROM "Audit"
WHERE severity NOT IN `+securitySeverities+` AND id <= (
  SELECT id FROM "Audit"
  WHERE severity NOT IN `+securitySeverities+`
  ORDER BY id DESC
  LIMIT 1 OFFSET $1
)`, s.MaxBackups); err != nil {
			return fmt.Errorf("audit: trim routine by count: %w", err)
		}
	}
	// Piggyback Revision retention onto the same ticker — a separate
	// leader-elected loop for one DELETE per 5min isn't worth it.
	// Best-effort: a prune failure here doesn't fail the audit trim.
	if _, err := s.DB.PruneRevisions(ctx); err != nil {
		slog.Warn("audit: prune revisions failed", "err", err)
	}
	return nil
}
