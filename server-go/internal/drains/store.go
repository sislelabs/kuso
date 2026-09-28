package drains

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

// keyPrefix namespaces drain rows in the generic Setting kv table: one
// row per drain ("drain.<id>" → JSON), so concurrent create/delete on
// different drains never read-modify-write the same row and no schema
// migration is needed. Drain counts are tiny (a handful per install).
const keyPrefix = "drain."

type sqlDB interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// SettingStore persists drains in the Setting table.
type SettingStore struct {
	DB sqlDB
}

// List returns every drain, oldest first.
func (s *SettingStore) List(ctx context.Context) ([]Drain, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT key, value FROM "Setting" WHERE key LIKE $1`, keyPrefix+"%")
	if err != nil {
		return nil, fmt.Errorf("drains: list: %w", err)
	}
	defer rows.Close()
	out := []Drain{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("drains: scan: %w", err)
		}
		var d Drain
		if err := json.Unmarshal([]byte(v), &d); err != nil {
			// One corrupt row must not stop every other drain.
			continue
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("drains: list: %w", err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// Get returns one drain or ErrNotFound.
func (s *SettingStore) Get(ctx context.Context, id string) (*Drain, error) {
	var v string
	err := s.DB.QueryRowContext(ctx, `SELECT value FROM "Setting" WHERE key = $1`, keyPrefix+id).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("drains: get: %w", err)
	}
	var d Drain
	if err := json.Unmarshal([]byte(v), &d); err != nil {
		return nil, fmt.Errorf("drains: decode %s: %w", id, err)
	}
	return &d, nil
}

// Put upserts d. An empty ID is assigned; timestamps are stamped.
func (s *SettingStore) Put(ctx context.Context, d *Drain, by string) error {
	now := time.Now().UTC()
	if d.ID == "" {
		id, err := newID()
		if err != nil {
			return err
		}
		d.ID, d.CreatedAt, d.CreatedBy = id, now, by
	}
	d.UpdatedAt = now
	b, err := json.Marshal(d)
	if err != nil {
		return fmt.Errorf("drains: encode: %w", err)
	}
	_, err = s.DB.ExecContext(ctx, `
		INSERT INTO "Setting" (key, value, "updatedAt", "updatedBy")
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (key) DO UPDATE
		   SET value = EXCLUDED.value,
		       "updatedAt" = EXCLUDED."updatedAt",
		       "updatedBy" = EXCLUDED."updatedBy"`,
		keyPrefix+d.ID, string(b), now, by)
	if err != nil {
		return fmt.Errorf("drains: put: %w", err)
	}
	return nil
}

// Delete removes a drain; ErrNotFound when it doesn't exist.
func (s *SettingStore) Delete(ctx context.Context, id string) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM "Setting" WHERE key = $1`, keyPrefix+id)
	if err != nil {
		return fmt.Errorf("drains: delete: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return nil
}

func newID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("drains: id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
