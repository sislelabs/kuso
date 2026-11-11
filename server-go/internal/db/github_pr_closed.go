package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// githubClosedPRRetention bounds how long a close tombstone is kept. A late
// delivery arrives within GitHub's retry window (24h); a week is slack.
const githubClosedPRRetention = 7 * 24 * time.Hour

// MarkGithubPRClosed records (or moves forward) the close time of repo#number
// and prunes expired tombstones. repo is matched case-insensitively.
func (d *DB) MarkGithubPRClosed(ctx context.Context, repo string, number int, closedAt time.Time) error {
	if _, err := d.ExecContext(ctx, `
INSERT INTO "GithubClosedPR" ("repo", "number", "closedAt") VALUES ($1, $2, $3)
ON CONFLICT ("repo", "number") DO UPDATE SET "closedAt" = GREATEST("GithubClosedPR"."closedAt", EXCLUDED."closedAt")`,
		strings.ToLower(repo), number, closedAt.UTC()); err != nil {
		return fmt.Errorf("db: mark github pr closed: %w", err)
	}
	if _, err := d.ExecContext(ctx, `DELETE FROM "GithubClosedPR" WHERE "closedAt" < $1`,
		time.Now().Add(-githubClosedPRRetention).UTC()); err != nil {
		return fmt.Errorf("db: prune github closed prs: %w", err)
	}
	return nil
}

// ClearGithubPRClosed drops repo#number's tombstone (the PR was reopened).
func (d *DB) ClearGithubPRClosed(ctx context.Context, repo string, number int) error {
	if _, err := d.ExecContext(ctx, `DELETE FROM "GithubClosedPR" WHERE "repo" = $1 AND "number" = $2`,
		strings.ToLower(repo), number); err != nil {
		return fmt.Errorf("db: clear github pr closed: %w", err)
	}
	return nil
}

// GithubPRClosedAt returns repo#number's recorded close time; ok=false when
// there is none.
func (d *DB) GithubPRClosedAt(ctx context.Context, repo string, number int) (time.Time, bool, error) {
	var at time.Time
	err := d.QueryRowContext(ctx, `SELECT "closedAt" FROM "GithubClosedPR" WHERE "repo" = $1 AND "number" = $2`,
		strings.ToLower(repo), number).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("db: github pr closed at: %w", err)
	}
	return at, true, nil
}
