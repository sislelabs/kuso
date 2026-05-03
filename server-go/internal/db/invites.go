package db

// Invite is the durable form of an invitation link. The token field
// is the URL-shareable secret; the rest configures what the invitee
// gets when they redeem (group + instance role override + expiry +
// max-uses cap).
//
// Lifecycle:
//   1. admin POSTs /api/invites with the configuration → CreateInvite
//      mints a row with usedCount=0
//   2. invitee GETs /api/invites/<token> → FindInviteByToken returns
//      a description (group name, expires, role) for the signup page
//   3. invitee finishes signup → RedeemInvite atomically increments
//      usedCount, inserts an InviteRedemption row, and (caller-side)
//      adds the user to the configured group
//   4. admin can list, revoke, or delete invites through the same
//      handler suite.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Invite is the wire/DB shape. JSON tags mirror what the HTTP
// handler emits so List can scan straight into [Invite] without a
// projection step.
type Invite struct {
	ID           string         `json:"id"`
	Token        string         `json:"token"`
	GroupID      sql.NullString `json:"groupId,omitempty"`
	InstanceRole sql.NullString `json:"instanceRole,omitempty"`
	CreatedBy    string         `json:"createdBy"`
	CreatedAt    time.Time      `json:"createdAt"`
	ExpiresAt    sql.NullTime   `json:"expiresAt,omitempty"`
	MaxUses      int            `json:"maxUses"`
	UsedCount    int            `json:"usedCount"`
	RevokedAt    sql.NullTime   `json:"revokedAt,omitempty"`
	Note         sql.NullString `json:"note,omitempty"`
}

// CreateInviteInput collapses the optional fields into a single
// constructor argument. Pointers distinguish "unset" from zero —
// e.g. ExpiresAt: nil means "never expires" while passing a zero
// time.Time would write 0000-01-01 into the db.
type CreateInviteInput struct {
	ID           string
	Token        string
	GroupID      *string
	InstanceRole *string
	CreatedBy    string
	ExpiresAt    *time.Time
	MaxUses      int
	Note         *string
}

// CreateInvite persists a fresh invite. Caller mints id + token; we
// don't generate them here so the random source stays in one place
// (auth.NewState equivalent in the handler layer).
func (d *DB) CreateInvite(ctx context.Context, in CreateInviteInput) error {
	maxUses := in.MaxUses
	if maxUses <= 0 {
		maxUses = 1
	}
	_, err := d.DB.ExecContext(ctx,
		`INSERT INTO "Invite"
			("id","token","groupId","instanceRole","createdBy","expiresAt","maxUses","note")
		 VALUES (?,?,?,?,?,?,?,?)`,
		in.ID, in.Token,
		nullStringFromPtr(in.GroupID),
		nullStringFromPtr(in.InstanceRole),
		in.CreatedBy,
		nullTimeFromPtr(in.ExpiresAt),
		maxUses,
		nullStringFromPtr(in.Note),
	)
	if err != nil {
		return fmt.Errorf("db: create invite: %w", err)
	}
	return nil
}

// FindInviteByToken loads an invite for redemption. Returns
// ErrNotFound when the token is unknown — the redemption handler
// maps that to 404, not 401, so probing tokens isn't useful.
func (d *DB) FindInviteByToken(ctx context.Context, token string) (*Invite, error) {
	row := d.DB.QueryRowContext(ctx,
		`SELECT id, token, "groupId", "instanceRole", "createdBy",
		        "createdAt", "expiresAt", "maxUses", "usedCount",
		        "revokedAt", note
		 FROM "Invite" WHERE token = ?`, token)
	var inv Invite
	err := row.Scan(&inv.ID, &inv.Token, &inv.GroupID, &inv.InstanceRole,
		&inv.CreatedBy, &inv.CreatedAt, &inv.ExpiresAt,
		&inv.MaxUses, &inv.UsedCount, &inv.RevokedAt, &inv.Note)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: find invite: %w", err)
	}
	return &inv, nil
}

// ListInvites returns all invites newest-first. Admin-only surface;
// the handler does the perm check.
func (d *DB) ListInvites(ctx context.Context) ([]Invite, error) {
	rows, err := d.DB.QueryContext(ctx,
		`SELECT id, token, "groupId", "instanceRole", "createdBy",
		        "createdAt", "expiresAt", "maxUses", "usedCount",
		        "revokedAt", note
		 FROM "Invite" ORDER BY "createdAt" DESC`)
	if err != nil {
		return nil, fmt.Errorf("db: list invites: %w", err)
	}
	defer rows.Close()
	out := make([]Invite, 0, 32)
	for rows.Next() {
		var inv Invite
		if err := rows.Scan(&inv.ID, &inv.Token, &inv.GroupID, &inv.InstanceRole,
			&inv.CreatedBy, &inv.CreatedAt, &inv.ExpiresAt,
			&inv.MaxUses, &inv.UsedCount, &inv.RevokedAt, &inv.Note); err != nil {
			return nil, fmt.Errorf("db: scan invite: %w", err)
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

// RevokeInvite stamps RevokedAt = now. We don't delete the row so
// the audit trail (who redeemed) survives — admins can later see
// who joined via a since-revoked link.
func (d *DB) RevokeInvite(ctx context.Context, id string) error {
	res, err := d.DB.ExecContext(ctx,
		`UPDATE "Invite" SET "revokedAt" = CURRENT_TIMESTAMP WHERE id = ? AND "revokedAt" IS NULL`,
		id)
	if err != nil {
		return fmt.Errorf("db: revoke invite: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteInvite removes the row entirely. Use sparingly — the
// redemption rows cascade away too. Most flows should prefer
// RevokeInvite.
func (d *DB) DeleteInvite(ctx context.Context, id string) error {
	res, err := d.DB.ExecContext(ctx, `DELETE FROM "Invite" WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("db: delete invite: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// RedeemInvite atomically validates and increments usage. Returns
// the invite row as it was BEFORE the increment so the caller has
// the configured groupId/role. Errors:
//   - ErrNotFound: token unknown
//   - ErrInviteExpired / ErrInviteExhausted / ErrInviteRevoked: the
//     invite isn't redeemable for the given reason
//
// The caller (HTTP handler) is responsible for actually creating the
// user, adding them to the group, and writing the InviteRedemption
// row via RecordRedemption — that's split out so a partial failure
// (e.g. group add fails after user create) doesn't leave a phantom
// usage count.
func (d *DB) RedeemInvite(ctx context.Context, token string) (*Invite, error) {
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("db: redeem tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	row := tx.QueryRowContext(ctx,
		`SELECT id, token, "groupId", "instanceRole", "createdBy",
		        "createdAt", "expiresAt", "maxUses", "usedCount",
		        "revokedAt", note
		 FROM "Invite" WHERE token = ?`, token)
	var inv Invite
	err = row.Scan(&inv.ID, &inv.Token, &inv.GroupID, &inv.InstanceRole,
		&inv.CreatedBy, &inv.CreatedAt, &inv.ExpiresAt,
		&inv.MaxUses, &inv.UsedCount, &inv.RevokedAt, &inv.Note)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: redeem read: %w", err)
	}
	if inv.RevokedAt.Valid {
		return nil, ErrInviteRevoked
	}
	if inv.ExpiresAt.Valid && inv.ExpiresAt.Time.Before(time.Now()) {
		return nil, ErrInviteExpired
	}
	if inv.UsedCount >= inv.MaxUses {
		return nil, ErrInviteExhausted
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE "Invite" SET "usedCount" = "usedCount" + 1 WHERE id = ?`, inv.ID); err != nil {
		return nil, fmt.Errorf("db: bump usedCount: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("db: redeem commit: %w", err)
	}
	return &inv, nil
}

// RecordRedemption logs a (invite, user) pair so admins can trace
// who joined via which link. Called by the redemption handler AFTER
// the user row + group attachment succeeded.
func (d *DB) RecordRedemption(ctx context.Context, inviteID, userID string) error {
	_, err := d.DB.ExecContext(ctx,
		`INSERT INTO "InviteRedemption" ("inviteId","userId") VALUES (?, ?)`,
		inviteID, userID)
	if err != nil {
		return fmt.Errorf("db: record redemption: %w", err)
	}
	return nil
}

// Sentinel errors the redeem flow returns. Mapped to HTTP status by
// the handler.
var (
	ErrInviteRevoked   = errors.New("invite: revoked")
	ErrInviteExpired   = errors.New("invite: expired")
	ErrInviteExhausted = errors.New("invite: usage cap reached")
)

// nullStringFromPtr converts a *string into a sql.NullString — nil
// pointer becomes NULL on the row. Tiny helper kept here so the
// invite file is self-contained.
func nullStringFromPtr(p *string) sql.NullString {
	if p == nil {
		return sql.NullString{}
	}
	return sql.NullString{Valid: true, String: *p}
}

// nullTimeFromPtr likewise for *time.Time.
func nullTimeFromPtr(p *time.Time) sql.NullTime {
	if p == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Valid: true, Time: *p}
}
