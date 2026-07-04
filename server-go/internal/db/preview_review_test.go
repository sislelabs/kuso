package db

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// SetPreviewReviewDecision must enforce the one-write rule the package
// documents: once a review is closed (PR merged/closed), a token holder
// can't flip the recorded decision. Pre-fix, the UPDATE had no
// closedAt guard and a closed review's decision could be rewritten. It
// must also disambiguate "token missing" (ErrNoRows) from "closed"
// (ErrReviewClosed) so the handler returns 404 vs 409 correctly.
func TestSetPreviewReviewDecision_ClosedGuard(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	// PreviewReview isn't in openTestDB's truncate set; clear it here.
	if _, err := d.ExecContext(ctx, `TRUNCATE TABLE "PreviewReview" RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate PreviewReview: %v", err)
	}

	rev, err := d.CreatePreviewReview(ctx, PreviewReview{
		Project: "p1", PRNumber: 7, PRTitle: "x", HeadRef: "feature", BaseRef: "main",
	})
	if err != nil {
		t.Fatalf("CreatePreviewReview: %v", err)
	}
	tok := rev.Token

	// Open review: a decision is accepted.
	if err := d.SetPreviewReviewDecision(ctx, tok, "approved", "lgtm", "rev@x"); err != nil {
		t.Fatalf("first decision on open review: %v", err)
	}

	// Unknown token → ErrNoRows (→ 404).
	if err := d.SetPreviewReviewDecision(ctx, "deadbeef"+tok, "denied", "", "rev@x"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("unknown token: got %v, want sql.ErrNoRows", err)
	}

	// Close the review (PR merged), then a decision flip must be rejected.
	if err := d.ClosePreviewReview(ctx, "p1", 7); err != nil {
		t.Fatalf("ClosePreviewReview: %v", err)
	}
	if err := d.SetPreviewReviewDecision(ctx, tok, "denied", "sneaky flip", "attacker"); !errors.Is(err, ErrReviewClosed) {
		t.Errorf("decision on closed review: got %v, want ErrReviewClosed", err)
	}

	// The original decision must survive the rejected flip.
	got, err := d.GetPreviewReviewByToken(ctx, tok)
	if err != nil {
		t.Fatalf("GetPreviewReviewByToken: %v", err)
	}
	if got.Decision != "approved" {
		t.Errorf("decision was mutated on a closed review: got %q, want approved", got.Decision)
	}
}
