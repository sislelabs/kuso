package db

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ServiceUsage is one five-minute CPU/memory bucket for a service env.
type ServiceUsage struct {
	Project  string
	Service  string
	Env      string
	Ts       time.Time
	CPUMilli int32
	MemBytes int64
	Pods     int32
}

// serviceUsageBatch keeps one INSERT's parameter count well under
// Postgres' 65535 limit (7 per row).
const serviceUsageBatch = 500

// SaveServiceUsage inserts buckets, skipping any another replica
// already wrote.
func (d *DB) SaveServiceUsage(ctx context.Context, rows []ServiceUsage) error {
	for len(rows) > 0 {
		n := min(len(rows), serviceUsageBatch)
		chunk := rows[:n]
		rows = rows[n:]
		var b strings.Builder
		b.WriteString(`INSERT INTO "ServiceUsage" ("project","service","env","ts","cpuMilli","memBytes","pods") VALUES `)
		args := make([]any, 0, n*7)
		for i, r := range chunk {
			if i > 0 {
				b.WriteByte(',')
			}
			p := i * 7
			fmt.Fprintf(&b, "($%d,$%d,$%d,$%d,$%d,$%d,$%d)", p+1, p+2, p+3, p+4, p+5, p+6, p+7)
			args = append(args, r.Project, r.Service, r.Env, r.Ts.UTC(), r.CPUMilli, r.MemBytes, r.Pods)
		}
		b.WriteString(` ON CONFLICT DO NOTHING`)
		if _, err := d.ExecContext(ctx, b.String(), args...); err != nil {
			return fmt.Errorf("save service usage: %w", err)
		}
	}
	return nil
}

// ListServiceUsageSince returns every bucket at or after since, oldest
// first.
func (d *DB) ListServiceUsageSince(ctx context.Context, since time.Time) ([]ServiceUsage, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT "project","service","env","ts","cpuMilli","memBytes","pods"
		  FROM "ServiceUsage"
		 WHERE "ts" >= $1
		 ORDER BY "ts"`, since.UTC())
	if err != nil {
		return nil, fmt.Errorf("list service usage: %w", err)
	}
	defer rows.Close()
	var out []ServiceUsage
	for rows.Next() {
		var r ServiceUsage
		if err := rows.Scan(&r.Project, &r.Service, &r.Env, &r.Ts, &r.CPUMilli, &r.MemBytes, &r.Pods); err != nil {
			return nil, fmt.Errorf("scan service usage: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PruneServiceUsageOlderThan deletes buckets before the cutoff.
func (d *DB) PruneServiceUsageOlderThan(ctx context.Context, before time.Time) (int64, error) {
	res, err := d.ExecContext(ctx, `DELETE FROM "ServiceUsage" WHERE "ts" < $1`, before.UTC())
	if err != nil {
		return 0, fmt.Errorf("prune service usage: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
