package db

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestGithubClosedPR_MarkReadClear(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	repo := fmt.Sprintf("Owner/Repo-%d", time.Now().UnixNano())
	closed := time.Now().UTC().Truncate(time.Second)

	if _, ok, err := d.GithubPRClosedAt(ctx, repo, 7); err != nil || ok {
		t.Fatalf("before close: ok=%v err=%v", ok, err)
	}
	if err := d.MarkGithubPRClosed(ctx, repo, 7, closed); err != nil {
		t.Fatal(err)
	}
	// An older redelivered close must not move the tombstone back.
	if err := d.MarkGithubPRClosed(ctx, repo, 7, closed.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	at, ok, err := d.GithubPRClosedAt(ctx, "owner/repo"+repo[len("Owner/Repo"):], 7)
	if err != nil || !ok || !at.Equal(closed) {
		t.Fatalf("after close: at=%v ok=%v err=%v", at, ok, err)
	}
	if err := d.ClearGithubPRClosed(ctx, repo, 7); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := d.GithubPRClosedAt(ctx, repo, 7); err != nil || ok {
		t.Fatalf("after reopen: ok=%v err=%v", ok, err)
	}
}
