package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// CreateUserInput is the field set the create-user handler accepts.
type CreateUserInput struct {
	ID            string
	Username      string
	Email         string
	FirstName     string
	LastName      string
	PasswordHash  string
	RoleID        string
	IsActive      bool
}

// CreateUser inserts a new user. PasswordHash must already be bcrypted —
// the handler is responsible for that, the DB layer never sees plaintext.
func (d *DB) CreateUser(ctx context.Context, in CreateUserInput) error {
	if in.ID == "" {
		return errors.New("db: user id required")
	}
	if in.Username == "" || in.Email == "" || in.PasswordHash == "" {
		return errors.New("db: username, email, password are required")
	}
	now := prismaNow()
	var roleID any = nil
	if in.RoleID != "" {
		roleID = in.RoleID
	}
	_, err := d.ExecContext(ctx, `
INSERT INTO "User" (id, username, email, "firstName", "lastName", password, "twoFaEnabled", "isActive", "roleId", provider, "createdAt", "updatedAt")
VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?, 'local', ?, ?)`,
		in.ID, in.Username, in.Email, sqlNullable(in.FirstName), sqlNullable(in.LastName),
		in.PasswordHash, in.IsActive, roleID, now, now,
	)
	if err != nil {
		return fmt.Errorf("db: create user: %w", err)
	}
	return nil
}

// UpdateUserInput is the partial-update payload for /api/users/id/:id.
// nil fields are left unchanged.
type UpdateUserInput struct {
	FirstName *string
	LastName  *string
	Email     *string
	RoleID    *string // empty string clears the role
	IsActive  *bool
	Image     *string
}

// UpdateUser applies the non-nil fields. Returns ErrNotFound when no row
// matches.
func (d *DB) UpdateUser(ctx context.Context, id string, in UpdateUserInput) error {
	sets := []string{`"updatedAt" = ?`}
	args := []any{prismaNow()}
	if in.FirstName != nil {
		sets = append(sets, `"firstName" = ?`)
		args = append(args, sqlNullable(*in.FirstName))
	}
	if in.LastName != nil {
		sets = append(sets, `"lastName" = ?`)
		args = append(args, sqlNullable(*in.LastName))
	}
	if in.Email != nil {
		sets = append(sets, `email = ?`)
		args = append(args, *in.Email)
	}
	if in.RoleID != nil {
		if *in.RoleID == "" {
			sets = append(sets, `"roleId" = NULL`)
		} else {
			sets = append(sets, `"roleId" = ?`)
			args = append(args, *in.RoleID)
		}
	}
	if in.IsActive != nil {
		sets = append(sets, `"isActive" = ?`)
		args = append(args, *in.IsActive)
	}
	if in.Image != nil {
		sets = append(sets, `image = ?`)
		args = append(args, sqlNullable(*in.Image))
	}
	args = append(args, id)
	q := `UPDATE "User" SET ` + joinComma(sets) + ` WHERE id = ?`
	res, err := d.ExecContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("db: update user: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteUser removes a user. The Prisma schema marks Audit.user and
// Token.userId as ON DELETE RESTRICT, so a naive DELETE fails as soon
// as the user has logged in once or issued a token. Clear the FK
// rows in the same transaction so the caller gets a usable result.
//
// _UserToUserGroup pivot rows have no RESTRICT but are cleared
// explicitly so the audit log shows what happened on user removal.
// GithubUserLink is dropped along with the user.
func (d *DB) DeleteUser(ctx context.Context, id string) error {
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("db: begin: %w", err)
	}
	for _, q := range []string{
		`DELETE FROM "Audit" WHERE user = ?`,
		`DELETE FROM "Token" WHERE "userId" = ?`,
		`DELETE FROM "_UserToUserGroup" WHERE "A" = ?`,
		`DELETE FROM "GithubUserLink" WHERE "userId" = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("db: delete user fks: %w", err)
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM "User" WHERE id = ?`, id)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("db: delete user: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		_ = tx.Rollback()
		return ErrNotFound
	}
	return tx.Commit()
}

// UpdateUserPassword writes a new password hash, bumping updatedAt.
func (d *DB) UpdateUserPassword(ctx context.Context, id, hash string) error {
	res, err := d.ExecContext(ctx, `UPDATE "User" SET password = ?, "updatedAt" = ? WHERE id = ?`,
		hash, prismaNow(), id)
	if err != nil {
		return fmt.Errorf("db: update password: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// sqlNullable returns sql.NullString{Valid:false} when in is empty so
// optional columns stay NULL rather than landing as "".
func sqlNullable(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func joinComma(in []string) string {
	out := ""
	for i, s := range in {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}
