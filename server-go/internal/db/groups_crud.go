package db

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// CreateGroup inserts a new UserGroup row.
func (d *DB) CreateGroup(ctx context.Context, id, name, description string) error {
	if id == "" || name == "" {
		return errors.New("db: id and name required")
	}
	now := time.Now().UTC()
	_, err := d.DB.ExecContext(ctx, `
INSERT INTO "UserGroup" (id, name, description, "createdAt", "updatedAt") VALUES (?, ?, ?, ?, ?)`,
		id, name, sqlNullable(description), now, now,
	)
	if err != nil {
		return fmt.Errorf("db: create group: %w", err)
	}
	return nil
}

// UpdateGroup replaces name + description.
func (d *DB) UpdateGroup(ctx context.Context, id, name, description string) error {
	res, err := d.DB.ExecContext(ctx, `
UPDATE "UserGroup" SET name = ?, description = ?, "updatedAt" = ? WHERE id = ?`,
		name, sqlNullable(description), time.Now().UTC(), id,
	)
	if err != nil {
		return fmt.Errorf("db: update group: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteGroup removes a group + its membership pivot rows.
func (d *DB) DeleteGroup(ctx context.Context, id string) error {
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("db: begin: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM "_UserToUserGroup" WHERE "B" = ?`, id); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("db: clear membership: %w", err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM "UserGroup" WHERE id = ?`, id)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("db: delete group: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		_ = tx.Rollback()
		return ErrNotFound
	}
	return tx.Commit()
}
