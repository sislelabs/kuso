// Webhook delivery dedup. A redelivery reuses the X-GitHub-Delivery
// UUID. The handler records a delivery only after its dispatch
// succeeded, so a replay of a handled delivery is a no-op while a
// redelivery of one that failed (or was lost on restart) runs again.
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
VALUES ($1, $2, $3)
ON CONFLICT ("deliveryId") DO NOTHING`,
		deliveryID, installationID, event,
	)
	if err != nil {
		return false, fmt.Errorf("db: github delivery seen: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 0, nil
}

// GithubDeliveryHandled reports whether a delivery id was already
// recorded, without recording it. The webhook handler records a delivery
// (SeenGithubDelivery) only once its dispatch succeeded.
func (d *DB) GithubDeliveryHandled(ctx context.Context, deliveryID string) (bool, error) {
	if deliveryID == "" {
		return false, nil
	}
	var n int
	err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM "GithubWebhookDelivery" WHERE "deliveryId" = $1`, deliveryID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("db: github delivery handled: %w", err)
	}
	return n > 0, nil
}

// PruneGithubDeliveries removes delivery rows older than `before`.
// Called from the daily cleanup goroutine.
func (d *DB) PruneGithubDeliveries(ctx context.Context, before time.Time) (int64, error) {
	res, err := d.ExecContext(ctx,
		`DELETE FROM "GithubWebhookDelivery" WHERE "receivedAt" < $1`,
		before.UTC(),
	)
	if err != nil {
		return 0, fmt.Errorf("db: prune github deliveries: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
