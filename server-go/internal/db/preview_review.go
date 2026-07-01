// Per-PR reviewer page state (v0.17.0). See schema.sql for the
// table definition. Rows are created by the dispatcher on PR open
// when at least one service in the project has spec.previews.reviewUrl=true.
// They're updated when the reviewer clicks Approve / Request Changes /
// Deny on the public reviewer page, then read by the dispatcher's
// PR-comment hook to post the decision back to GitHub.

package db

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// ErrInvalidDecision is returned by SetPreviewReviewDecision when the
// decision verb isn't one of the three accepted values. Handlers map
// it to 400 so the raw error text never needs to reach the client.
var ErrInvalidDecision = errors.New("invalid decision")

type PreviewReview struct {
	ID              string
	Project         string
	PRNumber        int
	PRTitle         string
	PRBody          string
	PRAuthor        string
	BaseRef         string
	HeadRef         string
	Token           string
	ReviewerEmail   string
	Decision        string // "" | "approved" | "changes_requested" | "denied"
	DecisionComment string
	DecidedAt       *time.Time
	DecidedBy       string
	CreatedAt       time.Time
	ClosedAt        *time.Time
}

// CreatePreviewReview inserts a new reviewer row + mints a fresh
// 32-byte hex token. Idempotent on (project, prNumber) — re-running
// for the same PR returns the existing row instead of creating a
// duplicate (handles PR sync events that re-fire the dispatcher).
func (d *DB) CreatePreviewReview(ctx context.Context, in PreviewReview) (*PreviewReview, error) {
	if existing, err := d.GetPreviewReviewByPR(ctx, in.Project, in.PRNumber); err == nil && existing != nil {
		return existing, nil
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("check existing review: %w", err)
	}
	if in.ID == "" {
		in.ID = randomHexID(16)
	}
	if in.Token == "" {
		in.Token = randomHexID(32)
	}
	if in.CreatedAt.IsZero() {
		in.CreatedAt = time.Now().UTC()
	}
	_, err := d.ExecContext(ctx, `
		INSERT INTO "PreviewReview"
		    (id, project, "prNumber", "prTitle", "prBody", "prAuthor",
		     "baseRef", "headRef", token, "reviewerEmail", "createdAt")
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`, in.ID, in.Project, in.PRNumber, in.PRTitle, in.PRBody, in.PRAuthor,
		in.BaseRef, in.HeadRef, in.Token, in.ReviewerEmail, in.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("insert review: %w", err)
	}
	return &in, nil
}

// GetPreviewReviewByToken is the path the public reviewer page uses.
// Token is the only credential the reviewer carries.
func (d *DB) GetPreviewReviewByToken(ctx context.Context, token string) (*PreviewReview, error) {
	return d.getOnePreviewReview(ctx, `token = $1`, token)
}

// GetPreviewReviewByPR is the dispatcher's lookup path on PR sync /
// PR close events.
func (d *DB) GetPreviewReviewByPR(ctx context.Context, project string, prNumber int) (*PreviewReview, error) {
	return d.getOnePreviewReview(ctx, `project = $1 AND "prNumber" = $2`, project, prNumber)
}

func (d *DB) getOnePreviewReview(ctx context.Context, whereClause string, args ...any) (*PreviewReview, error) {
	row := d.QueryRowContext(ctx, `
		SELECT id, project, "prNumber", "prTitle", "prBody", "prAuthor",
		       "baseRef", "headRef", token, "reviewerEmail",
		       decision, "decisionComment", "decidedAt", "decidedBy",
		       "createdAt", "closedAt"
		  FROM "PreviewReview"
		 WHERE `+whereClause+`
		 LIMIT 1
	`, args...)
	var r PreviewReview
	if err := row.Scan(
		&r.ID, &r.Project, &r.PRNumber, &r.PRTitle, &r.PRBody, &r.PRAuthor,
		&r.BaseRef, &r.HeadRef, &r.Token, &r.ReviewerEmail,
		&r.Decision, &r.DecisionComment, &r.DecidedAt, &r.DecidedBy,
		&r.CreatedAt, &r.ClosedAt,
	); err != nil {
		return nil, err
	}
	return &r, nil
}

// SetPreviewReviewDecision records the reviewer's choice. decision
// must be one of "approved" / "changes_requested" / "denied"; any
// other value is rejected so a typo in the public API can't poison
// the audit log. decidedBy is the reviewer's email (or "anonymous"
// when no email was provided — the URL is the only credential).
func (d *DB) SetPreviewReviewDecision(ctx context.Context, token, decision, comment, decidedBy string) error {
	switch decision {
	case "approved", "changes_requested", "denied":
	default:
		return fmt.Errorf("%w %q", ErrInvalidDecision, decision)
	}
	now := time.Now().UTC()
	res, err := d.ExecContext(ctx, `
		UPDATE "PreviewReview"
		   SET decision = $2,
		       "decisionComment" = $3,
		       "decidedAt" = $4,
		       "decidedBy" = $5
		 WHERE token = $1
	`, token, decision, comment, now, decidedBy)
	if err != nil {
		return fmt.Errorf("update review: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ClosePreviewReview stamps closedAt so the row sits in the audit
// log instead of the live-previews list. Called on PR merge/close.
// Doesn't delete the row — review history survives env-CR cleanup.
func (d *DB) ClosePreviewReview(ctx context.Context, project string, prNumber int) error {
	_, err := d.ExecContext(ctx, `
		UPDATE "PreviewReview"
		   SET "closedAt" = $3
		 WHERE project = $1 AND "prNumber" = $2 AND "closedAt" IS NULL
	`, project, prNumber, time.Now().UTC())
	return err
}

// randomHexID returns a hex-encoded random ID of the given byte
// length (so the string is 2x that long). Used for both row IDs and
// the reviewer-URL token.
func randomHexID(nBytes int) string {
	buf := make([]byte, nBytes)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}
