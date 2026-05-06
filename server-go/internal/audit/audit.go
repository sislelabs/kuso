// Package audit owns the Audit table writes + reads.
//
// Audit is opt-in via KUSO_AUDIT=true; when disabled, every method is a
// silent no-op so handler call sites don't need a guard.
package audit

import (
	"context"
	"fmt"
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
type Service struct {
	DB           *db.DB
	Enabled      bool
	MaxBackups   int

	mu sync.Mutex // guards the periodic trim() call
}

// New constructs a Service. KUSO_AUDIT=true enables, KUSO_AUDIT_LIMIT
// sets the row cap (default 1000), KUSO_AUDIT_TRIM_TIMEOUT overrides
// the per-tick context bound (default 60s — long enough for Postgres
// trim during an autovacuum pause on a 1M-row table).
//
// When enabled, New also starts the singleton trim ticker bound to
// ctx. Pass context.Background() in tests where you don't need
// shutdown semantics.
func New(ctx context.Context, d *db.DB) *Service {
	enabled := os.Getenv("KUSO_AUDIT") == "true"
	limit := 1000
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
	s := &Service{DB: d, Enabled: enabled, MaxBackups: limit}
	if enabled && d != nil {
		go s.runTrimLoop(ctx, trimTimeout)
	}
	return s
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
				fmt.Fprintf(os.Stderr, "audit: trim failed: %v\n", err)
			}
			cancel()
		}
	}
}

// Entry is one audit record. Fields default to empty strings so callers
// only fill in what they care about.
type Entry struct {
	User      string
	Severity  string
	Action    string
	Namespace string
	Phase     string
	App       string
	Pipeline  string
	Resource  string
	Message   string
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
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		now, e.Severity, e.Action, e.Namespace, e.Phase, e.App, e.Pipeline, e.Resource, e.Message, e.User, now, now,
	); err != nil {
		// Logging an audit row must never affect the call site — log the
		// failure to stderr-shaped slog and move on.
		fmt.Fprintf(os.Stderr, "audit: log failed: %v\n", err)
		return
	}
	// Trim runs on the singleton ticker started by New.
}

// Get returns the newest `limit` rows.
func (s *Service) Get(ctx context.Context, limit int) ([]Entry, int, error) {
	if s == nil || !s.Enabled {
		return nil, 0, nil
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.DB.QueryContext(ctx, `
SELECT timestamp, severity, action, namespace, phase, app, pipeline, resource, message, "user"
FROM "Audit" ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("audit: get: %w", err)
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		var ts time.Time
		if err := rows.Scan(&ts, &e.Severity, &e.Action, &e.Namespace, &e.Phase, &e.App, &e.Pipeline, &e.Resource, &e.Message, &e.User); err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var total int
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM "Audit"`).Scan(&total)
	return out, total, nil
}

// GetForProject returns audit rows filtered by project. The Audit
// table's `pipeline` column carries v0.1's pipeline name and v0.2's
// project name; both share the lifetime "this is the top-level
// container" semantics, so a single column is fine.
//
// Pagination is keyset on id: pass after=<id> to fetch the page
// older than that id. limit is clamped [1, 1000].
func (s *Service) GetForProject(ctx context.Context, project string, after int64, limit int) ([]Entry, int, error) {
	if s == nil || !s.Enabled {
		return nil, 0, nil
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	q := `
SELECT id, timestamp, severity, action, namespace, phase, app, pipeline, resource, message, "user"
FROM "Audit" WHERE pipeline = ?`
	args := []any{project}
	if after > 0 {
		q += " AND id < ?"
		args = append(args, after)
	}
	q += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("audit: get for project: %w", err)
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		var ts time.Time
		var id int64
		if err := rows.Scan(&id, &ts, &e.Severity, &e.Action, &e.Namespace, &e.Phase, &e.App, &e.Pipeline, &e.Resource, &e.Message, &e.User); err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var total int
	_ = s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM "Audit" WHERE pipeline = ?`,
		project).Scan(&total)
	return out, total, nil
}

// GetForApp returns the newest rows filtered by pipeline+phase+app.
func (s *Service) GetForApp(ctx context.Context, pipeline, phase, app string, limit int) ([]Entry, int, error) {
	if s == nil || !s.Enabled {
		return nil, 0, nil
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.DB.QueryContext(ctx, `
SELECT timestamp, severity, action, namespace, phase, app, pipeline, resource, message, "user"
FROM "Audit" WHERE pipeline = ? AND phase = ? AND app = ?
ORDER BY id DESC LIMIT ?`, pipeline, phase, app, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("audit: get app: %w", err)
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		var ts time.Time
		if err := rows.Scan(&ts, &e.Severity, &e.Action, &e.Namespace, &e.Phase, &e.App, &e.Pipeline, &e.Resource, &e.Message, &e.User); err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var total int
	_ = s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM "Audit" WHERE pipeline = ? AND phase = ? AND app = ?`,
		pipeline, phase, app).Scan(&total)
	return out, total, nil
}

// trim caps the table at MaxBackups rows. Returns nil when another
// trim is already in flight (TryLock miss). Errors propagate so the
// caller can log — silent failure was how the SQLite-only LIMIT
// syntax let the table grow unbounded.
//
// Perf note: on a 100k-row table the NOT IN subquery full-scans
// Audit. If that becomes a problem switch to a keyset
// `DELETE WHERE id < (SELECT MIN(id) FROM (… LIMIT N) t)` — also
// portable, faster on indexed id.
func (s *Service) trim(ctx context.Context) error {
	if !s.mu.TryLock() {
		return nil
	}
	defer s.mu.Unlock()
	_, err := s.DB.ExecContext(ctx, `
DELETE FROM "Audit" WHERE id NOT IN (
  SELECT id FROM "Audit" ORDER BY id DESC LIMIT ?
)`, s.MaxBackups)
	if err != nil {
		return fmt.Errorf("audit: trim: %w", err)
	}
	return nil
}
