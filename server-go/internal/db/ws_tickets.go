package db

import (
	"context"
	"fmt"
	"time"
)

// ConsumeWSTicket records a WebSocket ticket's jti and reports whether
// this was its first use. The PK makes the check atomic across replicas.
// Expired rows are pruned on the way in; a ticket can't verify past its
// expiry, so a pruned jti can't be replayed.
func (d *DB) ConsumeWSTicket(ctx context.Context, jti string, expiresAt time.Time) (bool, error) {
	if jti == "" {
		return false, nil
	}
	if _, err := d.ExecContext(ctx, `DELETE FROM "WSTicketUse" WHERE "expiresAt" < now() - interval '1 minute'`); err != nil {
		return false, fmt.Errorf("db: prune ws tickets: %w", err)
	}
	res, err := d.ExecContext(ctx,
		`INSERT INTO "WSTicketUse" ("jti", "expiresAt") VALUES ($1, $2) ON CONFLICT ("jti") DO NOTHING`,
		jti, expiresAt)
	if err != nil {
		return false, fmt.Errorf("db: consume ws ticket: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}
