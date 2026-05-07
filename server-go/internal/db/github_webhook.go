// Webhook delivery dedup. GitHub retries failed webhook deliveries
// (15min, 1h, 6h, 24h) reusing the same X-GitHub-Delivery UUID. With
// no server-side memory of seen ids, a flaky downstream (slow build
// pod create, kube apiserver hiccup) gets re-played for hours and
// triggers duplicate preview-env creates / build dispatches even
// though kuso-side dedup catches most of it. Recording the delivery
// id on first sight + 409-ing on replay closes the window.
//
// Retention: ~24h. GitHub's last retry happens at the 24h mark; rows
// older than that have no replay risk and we drop them in the daily
// cleanup goroutine.

package db

import (
	"context"
	"fmt"
	"time"
)

// SeenGithubDelivery records the delivery id and returns true when
// it was already present (i.e. this is a replay). The INSERT ...
// ON CONFLICT DO NOTHING + RowsAffected dance is atomic — we don't
// need a separate SELECT.
func (d *DB) SeenGithubDelivery(ctx context.Context, deliveryID, event string, installationID int64) (bool, error) {
	if deliveryID == "" {
		// No id header; can't dedup. Treat as never-seen so the
		// caller proceeds. GitHub always sends one in practice;
		// this is just for safety.
		return false, nil
	}
	res, err := d.ExecContext(ctx, `
INSERT INTO "GithubWebhookDelivery" ("deliveryId", "installationId", "event")
VALUES (?, ?, ?)
ON CONFLICT ("deliveryId") DO NOTHING`,
		deliveryID, installationID, event,
	)
	if err != nil {
		return false, fmt.Errorf("db: github delivery seen: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 0, nil
}

// PruneGithubDeliveries removes delivery rows older than `before`.
// Called from the daily cleanup goroutine.
func (d *DB) PruneGithubDeliveries(ctx context.Context, before time.Time) (int64, error) {
	res, err := d.ExecContext(ctx,
		`DELETE FROM "GithubWebhookDelivery" WHERE "receivedAt" < ?`,
		before.UTC(),
	)
	if err != nil {
		return 0, fmt.Errorf("db: prune github deliveries: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
