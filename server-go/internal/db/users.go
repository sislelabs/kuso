package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// User mirrors the columns the auth + users modules read. Optional fields
// in Prisma (NULLable in SQLite) use sql.Null* so we can tell apart "field
// is empty string" from "field is missing".
type User struct {
	ID            string
	Username      string
	FirstName     sql.NullString
	LastName      sql.NullString
	Email         string
	EmailVerified sql.NullTime // see scanUser below — backed by a prismaTime adapter
	Password      string
	TwoFaEnabled  bool
	TwoFaSecret   sql.NullString
	Image         sql.NullString
	RoleID        sql.NullString
	IsActive      bool
	LastLogin     sql.NullTime
	LastIP        sql.NullString
	Provider      sql.NullString
	ProviderID    sql.NullString
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// ErrNotFound is returned when a lookup yields no row.
var ErrNotFound = errors.New("db: not found")

const userColumns = `id, username, "firstName", "lastName", email, "emailVerified",
  password, "twoFaEnabled", "twoFaSecret", image, "roleId", "isActive",
  "lastLogin", "lastIp", provider, "providerId", "createdAt", "updatedAt"`

func scanUser(s interface {
	Scan(...any) error
}) (*User, error) {
	var u User
	var (
		emailVerified, lastLogin nullPrismaTime
		createdAt, updatedAt     prismaTime
	)
	if err := s.Scan(
		&u.ID, &u.Username, &u.FirstName, &u.LastName, &u.Email, &emailVerified,
		&u.Password, &u.TwoFaEnabled, &u.TwoFaSecret, &u.Image, &u.RoleID, &u.IsActive,
		&lastLogin, &u.LastIP, &u.Provider, &u.ProviderID, &createdAt, &updatedAt,
	); err != nil {
		return nil, err
	}
	u.EmailVerified = sql.NullTime{Time: emailVerified.Time, Valid: emailVerified.Valid}
	u.LastLogin = sql.NullTime{Time: lastLogin.Time, Valid: lastLogin.Valid}
	u.CreatedAt = createdAt.Time
	u.UpdatedAt = updatedAt.Time
	return &u, nil
}

// FindUserByUsername returns the User with the given username, or
// ErrNotFound. Username comparison is case-sensitive, matching Prisma's
// default and the existing TS behaviour.
func (d *DB) FindUserByUsername(ctx context.Context, username string) (*User, error) {
	row := d.DB.QueryRowContext(ctx, `SELECT `+userColumns+` FROM "User" WHERE username = ? LIMIT 1`, username)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: find user by username: %w", err)
	}
	return u, nil
}

// FindUserByEmail returns the User with the given email, or
// ErrNotFound. Used by signup paths to detect duplicates before
// creating a row (and to surface a clean 409 instead of a unique
// constraint error from the underlying DB).
func (d *DB) FindUserByEmail(ctx context.Context, email string) (*User, error) {
	row := d.DB.QueryRowContext(ctx, `SELECT `+userColumns+` FROM "User" WHERE email = ? LIMIT 1`, email)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: find user by email: %w", err)
	}
	return u, nil
}

// FindUserByID returns the User with the given id, or ErrNotFound.
func (d *DB) FindUserByID(ctx context.Context, id string) (*User, error) {
	row := d.DB.QueryRowContext(ctx, `SELECT `+userColumns+` FROM "User" WHERE id = ? LIMIT 1`, id)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: find user by id: %w", err)
	}
	return u, nil
}

// UpdateUserLogin records a successful login (lastLogin + lastIp).
//
// Best-effort — failure here MUST NOT block authentication, since the
// session is already established at the call site. Callers log and move
// on.
func (d *DB) UpdateUserLogin(ctx context.Context, userID, ip string, when time.Time) error {
	_, err := d.ExecContext(ctx,
		`UPDATE "User" SET "lastLogin" = ?, "lastIp" = ?, "updatedAt" = ? WHERE id = ?`,
		when, ip, when, userID,
	)
	if err != nil {
		return fmt.Errorf("db: update user login: %w", err)
	}
	return nil
}

// UserPermissions returns the role-scoped permissions for the given user
// as "<resource>:<action>" strings, matching the JWT permissions[] claim
// the TS server emits.
func (d *DB) UserPermissions(ctx context.Context, userID string) ([]string, error) {
	const q = `
SELECT p.resource || ':' || p.action
FROM "User" u
JOIN "Role" r ON r.id = u."roleId"
JOIN "_PermissionToRole" pr ON pr."B" = r.id
JOIN "Permission" p ON p.id = pr."A"
WHERE u.id = ?`
	rows, err := d.DB.QueryContext(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("db: user permissions: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("db: scan permission: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// UserRoleName returns the role name attached to the user, or "" if the
// user has no role. The JWT emits "none" in that case — that's a concern
// of the auth layer, not this query.
func (d *DB) UserRoleName(ctx context.Context, userID string) (string, error) {
	const q = `SELECT r.name FROM "User" u LEFT JOIN "Role" r ON r.id = u."roleId" WHERE u.id = ?`
	var name sql.NullString
	if err := d.DB.QueryRowContext(ctx, q, userID).Scan(&name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("db: user role name: %w", err)
	}
	if !name.Valid {
		return "", nil
	}
	return name.String, nil
}

// UserGroupNames returns the group names a user belongs to, in stable order.
func (d *DB) UserGroupNames(ctx context.Context, userID string) ([]string, error) {
	const q = `
SELECT g.name
FROM "_UserToUserGroup" ug
JOIN "UserGroup" g ON g.id = ug."B"
WHERE ug."A" = ?
ORDER BY g.name`
	rows, err := d.DB.QueryContext(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("db: user groups: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("db: scan group: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
