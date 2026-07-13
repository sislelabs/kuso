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
	_, err := d.ExecContext(ctx,
		`INSERT INTO "Invite"
			("id","token","groupId","instanceRole","createdBy","expiresAt","maxUses","note")
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
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
	row := d.QueryRowContext(ctx,
		`SELECT id, token, "groupId", "instanceRole", "createdBy",
		        "createdAt", "expiresAt", "maxUses", "usedCount",
		        "revokedAt", note
		 FROM "Invite" WHERE token = $1`, token)
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
	rows, err := d.QueryContext(ctx,
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
	res, err := d.ExecContext(ctx,
		`UPDATE "Invite" SET "revokedAt" = CURRENT_TIMESTAMP WHERE id = $1 AND "revokedAt" IS NULL`,
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
	res, err := d.ExecContext(ctx, `DELETE FROM "Invite" WHERE id = $1`, id)
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
// the invite row (usedCount reflects the state AFTER this claim).
// Errors:
//   - ErrNotFound: token unknown
//   - ErrInviteExpired / ErrInviteExhausted / ErrInviteRevoked: the
//     invite isn't redeemable for the given reason
//
// Prefer RedeemInviteNewUser / RedeemInviteExistingUser, which claim
// the seat AND apply user creation + membership + the redemption row
// in one transaction. This standalone form claims a seat with nothing
// attached to it — a failure in the caller afterwards burns the seat.
func (d *DB) RedeemInvite(ctx context.Context, token string) (*Invite, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("db: redeem tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	inv, err := consumeInviteTx(ctx, tx, token)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("db: redeem commit: %w", err)
	}
	return inv, nil
}

// consumeInviteTx claims one seat on the invite inside the caller's
// transaction. The claim is a single conditional UPDATE … RETURNING,
// so concurrent redemptions serialize on the row: the guard
// usedCount < maxUses is re-evaluated under the row lock and the loser
// of a race sees 0 rows instead of blowing past the cap (the old
// read-check-increment shape allowed usedCount > maxUses under
// concurrency). When the claim misses, the row is re-read to classify
// the failure into the sentinel errors.
func consumeInviteTx(ctx context.Context, tx *Tx, token string) (*Invite, error) {
	row := tx.QueryRowContext(ctx, `
		UPDATE "Invite" SET "usedCount" = "usedCount" + 1
		WHERE token = $1
		  AND "revokedAt" IS NULL
		  AND ("expiresAt" IS NULL OR "expiresAt" > CURRENT_TIMESTAMP)
		  AND "usedCount" < "maxUses"
		RETURNING id, token, "groupId", "instanceRole", "createdBy",
		          "createdAt", "expiresAt", "maxUses", "usedCount",
		          "revokedAt", note`, token)
	var inv Invite
	err := row.Scan(&inv.ID, &inv.Token, &inv.GroupID, &inv.InstanceRole,
		&inv.CreatedBy, &inv.CreatedAt, &inv.ExpiresAt,
		&inv.MaxUses, &inv.UsedCount, &inv.RevokedAt, &inv.Note)
	if err == nil {
		return &inv, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("db: claim invite seat: %w", err)
	}
	// Claim missed — re-read to say why.
	row = tx.QueryRowContext(ctx,
		`SELECT "expiresAt", "maxUses", "usedCount", "revokedAt"
		 FROM "Invite" WHERE token = $1`, token)
	err = row.Scan(&inv.ExpiresAt, &inv.MaxUses, &inv.UsedCount, &inv.RevokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: classify invite miss: %w", err)
	}
	switch {
	case inv.RevokedAt.Valid:
		return nil, ErrInviteRevoked
	case inv.ExpiresAt.Valid && inv.ExpiresAt.Time.Before(time.Now()):
		return nil, ErrInviteExpired
	case inv.UsedCount >= inv.MaxUses:
		return nil, ErrInviteExhausted
	}
	return nil, fmt.Errorf("db: invite %q not claimable for unknown reason", token)
}

// InviteNewUser is the account payload RedeemInviteNewUser creates.
// PasswordHash must already be bcrypted (handler's job).
type InviteNewUser struct {
	ID           string
	Username     string
	Email        string
	PasswordHash string
}

// RedeemInviteNewUser is the atomic local-signup redemption: seat
// claim, user creation, the invite's direct instance role, group
// membership (or pending fallback), and the InviteRedemption audit row
// all commit together. Any failure — duplicate username, missing
// group FK, whatever — rolls the whole thing back, so a failed signup
// neither burns a seat nor leaves a half-provisioned account.
func (d *DB) RedeemInviteNewUser(ctx context.Context, token string, user InviteNewUser) (*Invite, error) {
	if user.ID == "" || user.Username == "" || user.Email == "" || user.PasswordHash == "" {
		return nil, errors.New("db: redeem invite: id, username, email, password required")
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("db: redeem tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	inv, err := consumeInviteTx(ctx, tx, token)
	if err != nil {
		return nil, err
	}
	now := prismaNow()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO "User" (id, username, email, password, "twoFaEnabled", "isActive", provider, "createdAt", "updatedAt")
VALUES ($1, $2, $3, $4, false, true, 'local', $5, $6)`,
		user.ID, user.Username, user.Email, user.PasswordHash, now, now); err != nil {
		return nil, fmt.Errorf("db: redeem invite: create user: %w", err)
	}
	if err := applyInviteGrantsTx(ctx, tx, inv, user.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("db: redeem commit: %w", err)
	}
	d.EvictUserTenancy(user.ID)
	return inv, nil
}

// RedeemInviteExistingUser is the atomic redemption for a user who
// already has an account (the OAuth callback path — the user row is
// resolved/created by the provider-identity logic first). Seat claim,
// direct role, membership, and the redemption row commit together.
func (d *DB) RedeemInviteExistingUser(ctx context.Context, token, userID string) (*Invite, error) {
	if userID == "" {
		return nil, errors.New("db: redeem invite: userID required")
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("db: redeem tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	inv, err := consumeInviteTx(ctx, tx, token)
	if err != nil {
		return nil, err
	}
	if err := applyInviteGrantsTx(ctx, tx, inv, userID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("db: redeem commit: %w", err)
	}
	d.EvictUserTenancy(userID)
	return inv, nil
}

// applyInviteGrantsTx applies what the invite CONFIGURED — direct
// instance role, group membership (pending fallback when the invite
// carries no group), and the InviteRedemption audit row — inside the
// caller's transaction. The direct role is upgrade-only: an invite can
// raise a user's access to its configured level but never demote an
// existing admin who happens to click a viewer link.
func applyInviteGrantsTx(ctx context.Context, tx *Tx, inv *Invite, userID string) error {
	if inv.InstanceRole.Valid && inv.InstanceRole.String != "" {
		wanted := InstanceRole(inv.InstanceRole.String)
		var current sql.NullString
		if err := tx.QueryRowContext(ctx,
			`SELECT "instanceRole" FROM "User" WHERE id = $1`, userID).Scan(&current); err != nil {
			return fmt.Errorf("db: redeem invite: read instance role: %w", err)
		}
		if rankInstance(wanted) > rankInstance(InstanceRole(current.String)) {
			if _, err := tx.ExecContext(ctx,
				`UPDATE "User" SET "instanceRole" = $1, "updatedAt" = $2 WHERE id = $3`,
				string(wanted), prismaNow(), userID); err != nil {
				return fmt.Errorf("db: redeem invite: set instance role: %w", err)
			}
		}
	}
	if inv.GroupID.Valid && inv.GroupID.String != "" {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO "_UserToUserGroup" ("A", "B") VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			userID, inv.GroupID.String); err != nil {
			return fmt.Errorf("db: redeem invite: add to group: %w", err)
		}
	} else {
		// No group configured → pending, so an admin can find them.
		// Mirrors AddUserToPendingGroup but stays inside the tx.
		now := prismaNow()
		if _, err := tx.ExecContext(ctx, `
INSERT INTO "UserGroup" (id, name, description, "instanceRole", "projectMemberships", "createdAt", "updatedAt")
VALUES ('grp-pending', 'kuso-pending', 'users awaiting admin approval', 'pending', '[]', $1, $2)
ON CONFLICT DO NOTHING`, now, now); err != nil {
			return fmt.Errorf("db: redeem invite: ensure pending group: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO "_UserToUserGroup" ("A", "B") VALUES ($1, 'grp-pending') ON CONFLICT DO NOTHING`,
			userID); err != nil {
			return fmt.Errorf("db: redeem invite: add to pending: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO "InviteRedemption" ("inviteId","userId") VALUES ($1, $2)`,
		inv.ID, userID); err != nil {
		return fmt.Errorf("db: redeem invite: record redemption: %w", err)
	}
	return nil
}

// RecordRedemption logs a (invite, user) pair so admins can trace
// who joined via which link. Called by the redemption handler AFTER
// the user row + group attachment succeeded.
func (d *DB) RecordRedemption(ctx context.Context, inviteID, userID string) error {
	_, err := d.ExecContext(ctx,
		`INSERT INTO "InviteRedemption" ("inviteId","userId") VALUES ($1, $2)`,
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
