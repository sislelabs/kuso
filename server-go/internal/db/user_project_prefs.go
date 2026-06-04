// Per-user project preferences: starring (pin to top of the projects
// grid) and folder assignment (group projects under a free-text label).
// Stored server-side so the grid layout follows the user across devices,
// keyed by (userId, project). See migration 0003.

package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// UserProjectPref is one user's preference for one project. A row exists
// only when the user has expressed a preference (starred it or filed it
// in a folder); the default state (unstarred, unfiled) is the absence of
// a row, which keeps the table small and the "clear everything" path a
// plain DELETE.
type UserProjectPref struct {
	Project   string    `json:"project"`
	Starred   bool      `json:"starred"`
	Folder    string    `json:"folder,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ListUserProjectPrefs returns every preference row for one user. Returns
// an empty slice (not nil) when the user has set none, so JSON encodes as
// [] rather than null.
func (d *DB) ListUserProjectPrefs(ctx context.Context, userID string) ([]UserProjectPref, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT "project","starred","folder","updatedAt"
		FROM "UserProjectPref"
		WHERE "userId" = $1
		ORDER BY "project" ASC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list user project prefs: %w", err)
	}
	defer rows.Close()
	out := []UserProjectPref{}
	for rows.Next() {
		var p UserProjectPref
		var folder sql.NullString
		var updated prismaTime
		if err := rows.Scan(&p.Project, &p.Starred, &folder, &updated); err != nil {
			return nil, fmt.Errorf("scan user project pref: %w", err)
		}
		p.Folder = folder.String
		p.UpdatedAt = updated.Time
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// SetUserProjectPref upserts one (user, project) preference. An upsert to
// the default state (starred=false, folder="") deletes the row instead,
// so the table never accumulates no-op rows — keeps the "is this project
// starred/filed?" read a simple existence check and the table compact.
func (d *DB) SetUserProjectPref(ctx context.Context, userID, project string, starred bool, folder string) error {
	if starred == false && folder == "" {
		// Reverting to default — remove the row entirely.
		return d.ClearUserProjectPref(ctx, userID, project)
	}
	_, err := d.ExecContext(ctx, `
		INSERT INTO "UserProjectPref" ("userId","project","starred","folder","updatedAt")
		VALUES ($1,$2,$3,$4, now())
		ON CONFLICT ("userId","project") DO UPDATE
		  SET "starred" = EXCLUDED."starred",
		      "folder"  = EXCLUDED."folder",
		      "updatedAt" = now()`,
		userID, project, starred, nullableStr(folder),
	)
	if err != nil {
		return fmt.Errorf("upsert user project pref: %w", err)
	}
	return nil
}

// ClearUserProjectPref removes a user's preference for one project,
// reverting it to the default (unstarred, unfiled). Idempotent: a delete
// of a non-existent row is not an error.
func (d *DB) ClearUserProjectPref(ctx context.Context, userID, project string) error {
	_, err := d.ExecContext(ctx, `
		DELETE FROM "UserProjectPref" WHERE "userId" = $1 AND "project" = $2`,
		userID, project)
	if err != nil {
		return fmt.Errorf("clear user project pref: %w", err)
	}
	return nil
}

// RenameUserFolder moves every project the user filed under `from` to
// `to` across the user's prefs. An empty `to` unfiles them. Used by the
// rename-folder UI affordance. Returns the number of rows touched.
func (d *DB) RenameUserFolder(ctx context.Context, userID, from, to string) (int64, error) {
	if to == "" {
		// Unfiling. A row that is also unstarred reverts to the true
		// default (unstarred + unfiled) — which the "no row = default"
		// model says must be the ABSENCE of a row, not a NULL-folder row.
		// Leaving the row would resurface those projects in a phantom
		// "Unfiled" section. So DELETE the unstarred ones and only
		// NULL-out the folder on the starred ones (they keep their row
		// for the star). Two statements in a tx — a single CTE can't do
		// this because the DELETE and UPDATE would race on the same
		// snapshot and the UPDATE would resurrect the deleted rows.
		tx, err := d.BeginTx(ctx, nil)
		if err != nil {
			return 0, fmt.Errorf("unfile user folder (begin): %w", err)
		}
		defer func() { _ = tx.Rollback() }()
		del, err := tx.ExecContext(ctx, `
			DELETE FROM "UserProjectPref"
			WHERE "userId" = $1 AND "folder" = $2 AND "starred" = false`,
			userID, from)
		if err != nil {
			return 0, fmt.Errorf("unfile user folder (delete): %w", err)
		}
		upd, err := tx.ExecContext(ctx, `
			UPDATE "UserProjectPref"
			SET "folder" = NULL, "updatedAt" = now()
			WHERE "userId" = $1 AND "folder" = $2`,
			userID, from)
		if err != nil {
			return 0, fmt.Errorf("unfile user folder (update): %w", err)
		}
		if err := tx.Commit(); err != nil {
			return 0, fmt.Errorf("unfile user folder (commit): %w", err)
		}
		dn, _ := del.RowsAffected()
		un, _ := upd.RowsAffected()
		return dn + un, nil
	}
	res, err := d.ExecContext(ctx, `
		UPDATE "UserProjectPref"
		SET "folder" = $3, "updatedAt" = now()
		WHERE "userId" = $1 AND "folder" = $2`,
		userID, from, nullableStr(to))
	if err != nil {
		return 0, fmt.Errorf("rename user folder: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
