// Revision history for KusoService / KusoAddon / KusoEnvironment
// spec edits. Every UI save calls InsertRevision after a successful
// kube write so the History tab can render a chronological list of
// "what this CR looked like at each save", and Revert can replay the
// stored snapshot back through the existing update endpoint.
//
// Retention: rows older than RevisionRetention are deleted by the
// per-tick prune in the audit-trim loop (we piggyback on its
// scheduling rather than spinning up a third leader-elected ticker).

package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// RevisionRetention bounds how far back the history goes. 90 days is
// long enough for "what did I change last quarter?" and short enough
// that the table stays small even on chatty SaaS instances.
const RevisionRetention = 90 * 24 * time.Hour

// RevisionMaxPerCR caps the number of revisions we keep per CR. A
// runaway "save every keystroke" client could otherwise explode the
// table; this gives us a hard wall.
const RevisionMaxPerCR = 200

// Revision is one row. Snapshot is the full CR.spec as JSON — what
// the server would write to kube to reproduce this state.
type Revision struct {
	ID        string          `json:"id"`
	Project   string          `json:"project"`
	Kind      string          `json:"kind"`
	Name      string          `json:"name"`
	Actor     string          `json:"actor,omitempty"`
	Summary   string          `json:"summary,omitempty"`
	Snapshot  json.RawMessage `json:"snapshot"`
	CreatedAt time.Time       `json:"createdAt"`
}

// InsertRevision appends a row. The caller already validated the
// snapshot before writing it to kube; we don't re-parse here. Best-
// effort: a DB write failure logs but doesn't fail the user-facing
// save (the kube write already succeeded).
func (d *DB) InsertRevision(ctx context.Context, r Revision) error {
	if r.ID == "" {
		r.ID = uuid.NewString()
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	_, err := d.ExecContext(ctx, `
		INSERT INTO "Revision"("id", "project", "kind", "name", "actor", "summary", "snapshot", "createdAt")
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, r.ID, r.Project, r.Kind, r.Name, r.Actor, r.Summary, []byte(r.Snapshot), r.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert revision: %w", err)
	}
	// Cap per-CR rows. Cheap: index lookup + single DELETE statement.
	_, _ = d.ExecContext(ctx, `
		DELETE FROM "Revision"
		WHERE "id" IN (
			SELECT "id" FROM "Revision"
			WHERE "project" = $1 AND "kind" = $2 AND "name" = $3
			ORDER BY "createdAt" DESC
			OFFSET $4
		)
	`, r.Project, r.Kind, r.Name, RevisionMaxPerCR)
	return nil
}

// ListRevisions returns the most recent revisions for one CR, newest
// first. Bound at limit so the History tab can paginate.
func (d *DB) ListRevisions(ctx context.Context, project, kind, name string, limit int) ([]Revision, error) {
	if limit <= 0 || limit > RevisionMaxPerCR {
		limit = 50
	}
	rows, err := d.QueryContext(ctx, `
		SELECT "id", "project", "kind", "name", "actor", "summary", "snapshot", "createdAt"
		FROM "Revision"
		WHERE "project" = $1 AND "kind" = $2 AND "name" = $3
		ORDER BY "createdAt" DESC
		LIMIT $4
	`, project, kind, name, limit)
	if err != nil {
		return nil, fmt.Errorf("list revisions: %w", err)
	}
	defer rows.Close()
	out := make([]Revision, 0, limit)
	for rows.Next() {
		var r Revision
		var snap []byte
		if err := rows.Scan(&r.ID, &r.Project, &r.Kind, &r.Name, &r.Actor, &r.Summary, &snap, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan revision: %w", err)
		}
		r.Snapshot = json.RawMessage(snap)
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRevision loads one revision by id. Returned err wraps sql.ErrNoRows
// when the id doesn't exist so callers can distinguish "missing" from
// "DB error" via errors.Is.
func (d *DB) GetRevision(ctx context.Context, id string) (*Revision, error) {
	row := d.QueryRowContext(ctx, `
		SELECT "id", "project", "kind", "name", "actor", "summary", "snapshot", "createdAt"
		FROM "Revision" WHERE "id" = $1
	`, id)
	var r Revision
	var snap []byte
	if err := row.Scan(&r.ID, &r.Project, &r.Kind, &r.Name, &r.Actor, &r.Summary, &snap, &r.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("get revision: %w", err)
	}
	r.Snapshot = json.RawMessage(snap)
	return &r, nil
}

// PruneRevisions deletes rows older than RevisionRetention. Returns
// the number of rows removed. Called from the audit-trim ticker.
func (d *DB) PruneRevisions(ctx context.Context) (int64, error) {
	cutoff := time.Now().UTC().Add(-RevisionRetention)
	res, err := d.ExecContext(ctx, `DELETE FROM "Revision" WHERE "createdAt" < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune revisions: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
