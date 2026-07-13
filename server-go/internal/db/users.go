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
	row := d.QueryRowContext(ctx, `SELECT `+userColumns+` FROM "User" WHERE username = $1 LIMIT 1`, username)
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
	row := d.QueryRowContext(ctx, `SELECT `+userColumns+` FROM "User" WHERE email = $1 LIMIT 1`, email)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: find user by email: %w", err)
	}
	return u, nil
}

// FindUserByProvider returns the User linked to the given OAuth
// identity — the immutable (provider, providerID) pair — or
// ErrNotFound. This is the ONLY lookup the OAuth login path may use to
// resolve an existing account: usernames are mutable/reassignable on
// every provider, so matching on them lets a colliding identity
// authenticate as someone else's account.
func (d *DB) FindUserByProvider(ctx context.Context, provider, providerID string) (*User, error) {
	if provider == "" || providerID == "" {
		return nil, ErrNotFound
	}
	row := d.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM "User" WHERE provider = $1 AND "providerId" = $2 LIMIT 1`,
		provider, providerID)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: find user by provider: %w", err)
	}
	return u, nil
}

// LinkUserProvider stamps an OAuth identity onto an existing user row.
// Refuses (ErrConflictingProviderLink) when the user is already linked
// to a different identity — re-linking the SAME identity is an
// idempotent no-op. Callers must have verified the link is legitimate
// (see the auto-link rules in the OAuth handler) before calling.
func (d *DB) LinkUserProvider(ctx context.Context, userID, provider, providerID string) error {
	if userID == "" || provider == "" || providerID == "" {
		return errors.New("db: link provider: userID, provider, providerID required")
	}
	res, err := d.ExecContext(ctx, `
UPDATE "User" SET provider = $1, "providerId" = $2, "updatedAt" = $3
WHERE id = $4
  AND ("providerId" IS NULL OR (provider = $1 AND "providerId" = $2))`,
		provider, providerID, prismaNow(), userID)
	if err != nil {
		return fmt.Errorf("db: link user provider: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Either the user vanished or they're linked to a different
		// identity already. Disambiguate for the caller.
		if _, ferr := d.FindUserByID(ctx, userID); errors.Is(ferr, ErrNotFound) {
			return ErrNotFound
		}
		return fmt.Errorf("%w: user %s already linked to another identity", ErrConflictingProviderLink, userID)
	}
	return nil
}

// ErrConflictingProviderLink is returned when a provider-link write
// would overwrite a different existing OAuth identity on the user.
var ErrConflictingProviderLink = errors.New("db: user linked to a different provider identity")

// CreateOAuthUserInput is the field set for OAuth-originated signups.
// PasswordHash must be the stub hash (see auth.StubPasswordCost) — the
// account is OAuth-only until the user sets a real password.
type CreateOAuthUserInput struct {
	ID           string
	Username     string
	Email        string
	PasswordHash string
	Provider     string
	ProviderID   string
}

// CreateOAuthUser inserts a user row that carries its OAuth identity
// from birth, so subsequent logins resolve by (provider, providerId)
// instead of the mutable username. Kept separate from CreateUser
// (which hardcodes provider='local') so the admin create-user handler
// keeps its shape.
func (d *DB) CreateOAuthUser(ctx context.Context, in CreateOAuthUserInput) error {
	if in.ID == "" || in.Username == "" || in.Email == "" || in.PasswordHash == "" {
		return errors.New("db: create oauth user: id, username, email, password required")
	}
	if in.Provider == "" || in.ProviderID == "" {
		return errors.New("db: create oauth user: provider identity required")
	}
	now := prismaNow()
	_, err := d.ExecContext(ctx, `
INSERT INTO "User" (id, username, email, password, "twoFaEnabled", "isActive", provider, "providerId", "createdAt", "updatedAt")
VALUES ($1, $2, $3, $4, false, true, $5, $6, $7, $8)`,
		in.ID, in.Username, in.Email, in.PasswordHash, in.Provider, in.ProviderID, now, now)
	if err != nil {
		return fmt.Errorf("db: create oauth user: %w", err)
	}
	return nil
}

// FindUserIDByGithubLink resolves the kuso user linked to a GitHub
// account through the GithubUserLink table (written on every GitHub
// OAuth login and by the explicit repo-access link flow). Used as the
// migration path for accounts created before the User row recorded
// (provider, providerId): a GithubUserLink row proves this exact
// GitHub identity authenticated as (or was linked by) that user
// before, so auto-linking is safe.
func (d *DB) FindUserIDByGithubLink(ctx context.Context, githubID int64) (string, error) {
	var userID string
	err := d.QueryRowContext(ctx,
		`SELECT "userId" FROM "GithubUserLink" WHERE "githubId" = $1 LIMIT 1`, githubID).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("db: find user by github link: %w", err)
	}
	return userID, nil
}

// FindUserByID returns the User with the given id, or ErrNotFound.
func (d *DB) FindUserByID(ctx context.Context, id string) (*User, error) {
	row := d.QueryRowContext(ctx, `SELECT `+userColumns+` FROM "User" WHERE id = $1 LIMIT 1`, id)
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
		`UPDATE "User" SET "lastLogin" = $1, "lastIp" = $2, "updatedAt" = $3 WHERE id = $4`,
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
WHERE u.id = $1`
	rows, err := d.QueryContext(ctx, q, userID)
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
	const q = `SELECT r.name FROM "User" u LEFT JOIN "Role" r ON r.id = u."roleId" WHERE u.id = $1`
	var name sql.NullString
	if err := d.QueryRowContext(ctx, q, userID).Scan(&name); err != nil {
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
WHERE ug."A" = $1
ORDER BY g.name`
	rows, err := d.QueryContext(ctx, q, userID)
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
