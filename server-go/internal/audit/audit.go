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
// with New; safe for concurrent use.
type Service struct {
	DB         *db.DB
	Enabled    bool
	MaxBackups int

	mu sync.Mutex // guards the periodic trim() call
}

// New constructs a Service. KUSO_AUDIT=true enables, KUSO_AUDIT_LIMIT
// sets the row cap (default 1000).
//
// When enabled, New also starts a single trim ticker. Pre-v0.9.9 every
// audit Log spawned `go s.trim(context.Background())` — under a build
// storm (1k events/s) we'd leak 1k goroutines all racing the TryLock,
// each pinning a Background ctx forever. One ticker is enough; the
// table cap is a coarse signal anyway.
func New(d *db.DB) *Service {
	enabled := os.Getenv("KUSO_AUDIT") == "true"
	limit := 1000
	if v := os.Getenv("KUSO_AUDIT_LIMIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	s := &Service{DB: d, Enabled: enabled, MaxBackups: limit}
	if enabled && d != nil {
		go s.runTrimLoop()
	}
	return s
}

// runTrimLoop is the singleton trim ticker — fires every 5 minutes.
// Called once at construction; not exposed for re-entry.
func (s *Service) runTrimLoop() {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for range t.C {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		s.trim(ctx)
		cancel()
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
	// `user` is reserved on Postgres — unquoted it resolves to
	// current_user() and the INSERT silently fails the FK check.
	// camelCase column names also need quoting on Postgres. Same
	// fix as queries.go ListAudit.
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
	// Trim is on a singleton ticker — see runTrimLoop in New. The
	// pre-v0.9.9 per-write goroutine spawn pattern is gone.
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

// trim caps the table at MaxBackups rows, deleting oldest. Best-effort;
// happens off the request path.
//
// Pre-v0.9.9 used `LIMIT -1 OFFSET ?` — SQLite-only syntax that
// Postgres rejects with a parse error, so trim was a silent no-op and
// the table grew unbounded. NOT IN against the keep-set is portable.
func (s *Service) trim(ctx context.Context) {
	if !s.mu.TryLock() {
		return
	}
	defer s.mu.Unlock()
	_, _ = s.DB.ExecContext(ctx, `
DELETE FROM "Audit" WHERE id NOT IN (
  SELECT id FROM "Audit" ORDER BY id DESC LIMIT ?
)`, s.MaxBackups)
}
