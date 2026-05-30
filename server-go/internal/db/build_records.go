// Build summary archive (deployment history). Companion to
// build_logs.go: where BuildLog keeps the log tail, BuildRecord keeps
// the build's SUMMARY (commit, image, status, timing, who triggered)
// so the Deployments tab can show past builds after the retention
// cleanup deletes the KusoBuild CR. One row per build, keyed on the CR
// name (stable, unique per build). The summary is tiny (~a few hundred
// bytes), so retention here is generous — we keep the record long after
// the CR and even the log tail are gone.

package db

import (
	"context"
	"fmt"
	"time"
)

// BuildRecord is the archived summary of one finished build. Field set
// mirrors the handler's buildSummary so the read path can reconstruct a
// row identical to a live-CR-derived one.
type BuildRecord struct {
	BuildName       string
	Project         string
	Service         string
	Branch          string
	CommitSha       string
	CommitMessage   string
	ImageTag        string
	Status          string
	StartedAt       string
	FinishedAt      string
	TriggeredBy     string
	TriggeredByUser string
	ErrorMessage    string
	CreatedAt       time.Time
}

// SaveBuildRecord upserts a build's summary. Called from the build
// status poller at the same terminal-phase transition that archives the
// log tail. Upsert (not insert) so a re-poll of the same terminal build
// refreshes the row rather than erroring.
func (d *DB) SaveBuildRecord(ctx context.Context, r BuildRecord) error {
	if r.BuildName == "" {
		return fmt.Errorf("SaveBuildRecord: empty buildName")
	}
	_, err := d.ExecContext(ctx, `
		INSERT INTO "BuildRecord"(
			"buildName","project","service","branch","commitSha","commitMessage",
			"imageTag","status","startedAt","finishedAt","triggeredBy",
			"triggeredByUser","errorMessage")
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT("buildName") DO UPDATE SET
			"project"=excluded."project",
			"service"=excluded."service",
			"branch"=excluded."branch",
			"commitSha"=excluded."commitSha",
			"commitMessage"=excluded."commitMessage",
			"imageTag"=excluded."imageTag",
			"status"=excluded."status",
			"startedAt"=excluded."startedAt",
			"finishedAt"=excluded."finishedAt",
			"triggeredBy"=excluded."triggeredBy",
			"triggeredByUser"=excluded."triggeredByUser",
			"errorMessage"=excluded."errorMessage"
	`, r.BuildName, r.Project, r.Service, r.Branch, r.CommitSha, r.CommitMessage,
		r.ImageTag, r.Status, r.StartedAt, r.FinishedAt, r.TriggeredBy,
		r.TriggeredByUser, r.ErrorMessage)
	if err != nil {
		return fmt.Errorf("SaveBuildRecord: %w", err)
	}
	return nil
}

// ListBuildRecords returns archived build summaries for a service,
// newest-first by createdAt. Used by the Deployments list to backfill
// builds whose live CR has been GC'd.
func (d *DB) ListBuildRecords(ctx context.Context, project, service string) ([]BuildRecord, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT "buildName","project","service","branch","commitSha","commitMessage",
		       "imageTag","status","startedAt","finishedAt","triggeredBy",
		       "triggeredByUser","errorMessage","createdAt"
		  FROM "BuildRecord"
		 WHERE "project"=? AND "service"=?
		 ORDER BY "createdAt" DESC`, project, service)
	if err != nil {
		return nil, fmt.Errorf("ListBuildRecords: %w", err)
	}
	defer rows.Close()
	var out []BuildRecord
	for rows.Next() {
		var r BuildRecord
		if err := rows.Scan(&r.BuildName, &r.Project, &r.Service, &r.Branch,
			&r.CommitSha, &r.CommitMessage, &r.ImageTag, &r.Status, &r.StartedAt,
			&r.FinishedAt, &r.TriggeredBy, &r.TriggeredByUser, &r.ErrorMessage,
			&r.CreatedAt); err != nil {
			return nil, fmt.Errorf("ListBuildRecords scan: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteBuildRecordsForService removes archived summaries for a service
// — called on service delete so history doesn't outlive the service.
// Mirrors DeleteBuildLogsForService.
func (d *DB) DeleteBuildRecordsForService(ctx context.Context, project, service string) error {
	_, err := d.ExecContext(ctx,
		`DELETE FROM "BuildRecord" WHERE "project"=? AND "service"=?`,
		project, service)
	if err != nil {
		return fmt.Errorf("DeleteBuildRecordsForService: %w", err)
	}
	return nil
}
