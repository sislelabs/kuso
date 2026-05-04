// SSH key library. The kuso server can generate keypairs server-side
// (ed25519 by default) so the operator never types or pastes a private
// key — they paste the public half into the new VM's authorized_keys
// and we use the matching private half to connect. Same key can be
// reused across multiple node joins.
//
// Why store the private key in SQLite instead of a kube Secret: kuso-
// server is single-replica + has its own SQLite PVC; the keys live on
// the same node anyway. A kube Secret would add a hop without changing
// the security model.

package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type SSHKey struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	PublicKey   string    `json:"publicKey"`
	PrivateKey  string    `json:"-"` // never sent on the wire
	Fingerprint string    `json:"fingerprint"`
	CreatedAt   time.Time `json:"createdAt"`
}

// CreateSSHKey inserts a new key. The handler is responsible for
// generating + fingerprinting before passing the row in.
func (d *DB) CreateSSHKey(ctx context.Context, k SSHKey) error {
	_, err := d.ExecContext(ctx, `
		INSERT INTO "SSHKey" ("id","name","publicKey","privateKey","fingerprint")
		VALUES (?,?,?,?,?)`,
		k.ID, k.Name, k.PublicKey, k.PrivateKey, k.Fingerprint,
	)
	if err != nil {
		return fmt.Errorf("insert ssh key: %w", err)
	}
	return nil
}

// ListSSHKeys returns every stored key without the private bytes.
func (d *DB) ListSSHKeys(ctx context.Context) ([]SSHKey, error) {
	rows, err := d.DB.QueryContext(ctx, `
		SELECT "id","name","publicKey","fingerprint","createdAt"
		FROM "SSHKey"
		ORDER BY "createdAt" DESC`)
	if err != nil {
		return nil, fmt.Errorf("list ssh keys: %w", err)
	}
	defer rows.Close()
	out := []SSHKey{}
	for rows.Next() {
		var k SSHKey
		var ts sql.NullTime
		if err := rows.Scan(&k.ID, &k.Name, &k.PublicKey, &k.Fingerprint, &ts); err != nil {
			return nil, fmt.Errorf("scan ssh key: %w", err)
		}
		if ts.Valid {
			k.CreatedAt = ts.Time
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// GetSSHKey returns a key by id, including the private bytes — only
// callers inside the server (the join flow) should use this.
func (d *DB) GetSSHKey(ctx context.Context, id string) (*SSHKey, error) {
	row := d.DB.QueryRowContext(ctx, `
		SELECT "id","name","publicKey","privateKey","fingerprint","createdAt"
		FROM "SSHKey" WHERE "id" = ?`, id)
	var k SSHKey
	var ts sql.NullTime
	if err := row.Scan(&k.ID, &k.Name, &k.PublicKey, &k.PrivateKey, &k.Fingerprint, &ts); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get ssh key: %w", err)
	}
	if ts.Valid {
		k.CreatedAt = ts.Time
	}
	return &k, nil
}

// DeleteSSHKey removes a key. Doesn't cascade — joined nodes don't
// need the key after install (k3s agent has its own kube creds).
func (d *DB) DeleteSSHKey(ctx context.Context, id string) error {
	res, err := d.ExecContext(ctx, `DELETE FROM "SSHKey" WHERE "id" = ?`, id)
	if err != nil {
		return fmt.Errorf("delete ssh key: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
