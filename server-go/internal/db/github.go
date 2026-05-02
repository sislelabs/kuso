package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// GithubInstallation mirrors the Prisma GithubInstallation model.
type GithubInstallation struct {
	ID              int64
	AccountLogin    string
	AccountType     string
	AccountID       int64
	RepositoriesJSON string // JSON-encoded []GithubRepo
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// GithubRepo is the cached repo shape stored in
// GithubInstallation.repositoriesJson.
type GithubRepo struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	FullName      string `json:"fullName"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"defaultBranch"`
}

// GithubUserLink mirrors the Prisma GithubUserLink model.
type GithubUserLink struct {
	ID          string
	UserID      string
	GithubLogin string
	GithubID    int64
	AccessToken sql.NullString
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// UpsertGithubInstallation writes or replaces a GithubInstallation row.
func (d *DB) UpsertGithubInstallation(ctx context.Context, in GithubInstallation) error {
	if in.ID == 0 {
		return errors.New("db: github installation id required")
	}
	now := time.Now().UTC()
	if in.CreatedAt.IsZero() {
		in.CreatedAt = now
	}
	if in.RepositoriesJSON == "" {
		in.RepositoriesJSON = "[]"
	}
	_, err := d.DB.ExecContext(ctx, `
INSERT INTO "GithubInstallation" (id, "accountLogin", "accountType", "accountId", "repositoriesJson", "createdAt", "updatedAt")
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  "accountLogin" = excluded."accountLogin",
  "accountType" = excluded."accountType",
  "accountId" = excluded."accountId",
  "repositoriesJson" = excluded."repositoriesJson",
  "updatedAt" = excluded."updatedAt"`,
		in.ID, in.AccountLogin, in.AccountType, in.AccountID, in.RepositoriesJSON, in.CreatedAt, now,
	)
	if err != nil {
		return fmt.Errorf("db: upsert github installation: %w", err)
	}
	return nil
}

// SetGithubInstallationRepos replaces only the repositoriesJson column
// for an existing installation.
func (d *DB) SetGithubInstallationRepos(ctx context.Context, id int64, repos []GithubRepo) error {
	if repos == nil {
		repos = []GithubRepo{}
	}
	body, err := json.Marshal(repos)
	if err != nil {
		return fmt.Errorf("db: encode repos: %w", err)
	}
	res, err := d.DB.ExecContext(ctx, `
UPDATE "GithubInstallation" SET "repositoriesJson" = ?, "updatedAt" = ? WHERE id = ?`,
		string(body), time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("db: set installation repos: %w", err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteGithubInstallation removes an installation row.
func (d *DB) DeleteGithubInstallation(ctx context.Context, id int64) error {
	if _, err := d.DB.ExecContext(ctx, `DELETE FROM "GithubInstallation" WHERE id = ?`, id); err != nil {
		return fmt.Errorf("db: delete github installation: %w", err)
	}
	return nil
}

// ListGithubInstallations returns every cached installation.
func (d *DB) ListGithubInstallations(ctx context.Context) ([]GithubInstallation, error) {
	rows, err := d.DB.QueryContext(ctx, `
SELECT id, "accountLogin", "accountType", "accountId", "repositoriesJson", "createdAt", "updatedAt"
FROM "GithubInstallation" ORDER BY "accountLogin"`)
	if err != nil {
		return nil, fmt.Errorf("db: list github installations: %w", err)
	}
	defer rows.Close()
	var out []GithubInstallation
	for rows.Next() {
		var g GithubInstallation
		if err := rows.Scan(&g.ID, &g.AccountLogin, &g.AccountType, &g.AccountID, &g.RepositoriesJSON, &g.CreatedAt, &g.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// GithubInstallationRepos returns the cached repos for one installation.
func (d *DB) GithubInstallationRepos(ctx context.Context, id int64) ([]GithubRepo, error) {
	var raw string
	err := d.DB.QueryRowContext(ctx, `SELECT "repositoriesJson" FROM "GithubInstallation" WHERE id = ?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: read installation repos: %w", err)
	}
	var out []GithubRepo
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("db: decode repos: %w", err)
	}
	return out, nil
}

// UpsertGithubUserLink links a kuso user to a GitHub account.
func (d *DB) UpsertGithubUserLink(ctx context.Context, link GithubUserLink) error {
	if link.UserID == "" || link.GithubID == 0 {
		return errors.New("db: userId and githubId required")
	}
	if link.ID == "" {
		link.ID = mustRandomID()
	}
	now := time.Now().UTC()
	if link.CreatedAt.IsZero() {
		link.CreatedAt = now
	}
	_, err := d.DB.ExecContext(ctx, `
INSERT INTO "GithubUserLink" (id, "userId", "githubLogin", "githubId", "accessToken", "createdAt", "updatedAt")
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT("userId") DO UPDATE SET
  "githubLogin" = excluded."githubLogin",
  "githubId" = excluded."githubId",
  "accessToken" = excluded."accessToken",
  "updatedAt" = excluded."updatedAt"`,
		link.ID, link.UserID, link.GithubLogin, link.GithubID, link.AccessToken, link.CreatedAt, now,
	)
	if err != nil {
		return fmt.Errorf("db: upsert github user link: %w", err)
	}
	return nil
}
