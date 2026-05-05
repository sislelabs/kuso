// Build log archive. The kaniko Job pod's logs are reaped when the Job
// hits its ttlSecondsAfterFinished (1h post-completion). After that,
// the deployments-tab "expand" reveals nothing — the user can't read
// why a yesterday's build failed without ssh'ing the cluster. We
// snapshot the last N lines at terminal-phase transition (succeeded /
// failed / cancelled) so historical logs survive pod GC.
//
// One row per build, keyed on the KusoBuild CR name (stable, unique
// per build). `logs` is the joined tail; ~200 lines × ~120 chars each
// caps each row at ~25 KB. With cluster-cap 50 active services × 100
// builds retained, total worst-case is ~125 MB — well within the
// SQLite file's lifecycle.

package db

import (
	"context"
	"database/sql"
	"fmt"
)

// SaveBuildLog upserts the log tail for a build. Called from the build
// status poller on the queued→done edge.
func (d *DB) SaveBuildLog(ctx context.Context, buildName, project, service, phase, logs string) error {
	if buildName == "" {
		return fmt.Errorf("SaveBuildLog: empty buildName")
	}
	_, err := d.DB.ExecContext(ctx, `
		INSERT INTO "BuildLog"("buildName","project","service","phase","logs")
		VALUES(?, ?, ?, ?, ?)
		ON CONFLICT("buildName") DO UPDATE SET
			"project"=excluded."project",
			"service"=excluded."service",
			"phase"=excluded."phase",
			"logs"=excluded."logs"
	`, buildName, project, service, phase, logs)
	if err != nil {
		return fmt.Errorf("SaveBuildLog: %w", err)
	}
	return nil
}

// GetBuildLog returns the archived tail for a build. Returns "" if
// no row exists (the pod might still be alive — caller should fall
// back to streaming).
func (d *DB) GetBuildLog(ctx context.Context, buildName string) (string, error) {
	var logs string
	err := d.DB.QueryRowContext(ctx,
		`SELECT "logs" FROM "BuildLog" WHERE "buildName"=?`, buildName,
	).Scan(&logs)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("GetBuildLog: %w", err)
	}
	return logs, nil
}

// DeleteBuildLogsForService removes archived logs for a service —
// called when the service is deleted so we don't keep dead rows
// pointing at a service the user has forgotten about.
func (d *DB) DeleteBuildLogsForService(ctx context.Context, project, service string) error {
	_, err := d.DB.ExecContext(ctx,
		`DELETE FROM "BuildLog" WHERE "project"=? AND "service"=?`,
		project, service,
	)
	if err != nil {
		return fmt.Errorf("DeleteBuildLogsForService: %w", err)
	}
	return nil
}
