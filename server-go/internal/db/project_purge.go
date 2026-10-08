package db

import (
	"context"
	"errors"
	"fmt"
)

// PurgeProjectState deletes the project-scoped rows that would otherwise
// re-attach to a new project created under the same name: alert rules,
// runtime logs, error events and uptime state. Build history has its own
// deleters (DeleteBuildRecordsForProject / DeleteBuildLogsForProject).
// Every table is attempted; failures are joined.
func (d *DB) PurgeProjectState(ctx context.Context, project string) error {
	var errs []error
	for _, table := range []string{"AlertRule", "LogLine", "ErrorEvent", "UptimeState"} {
		if _, err := d.ExecContext(ctx, `DELETE FROM "`+table+`" WHERE "project"=$1`, project); err != nil {
			errs = append(errs, fmt.Errorf("purge %s: %w", table, err))
		}
	}
	return errors.Join(errs...)
}
