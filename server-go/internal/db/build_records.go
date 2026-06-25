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
	"database/sql"
	"errors"
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
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
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

// buildRecordCap bounds ListBuildRecords. BuildRecord is the permanent
// post-GC archive; a service on continuous deploys accumulates thousands
// of rows, and the Deployments tab fetched ALL of them on every open
// (then dedup'd in Go against live CRs). The newest ~100 cover every
// realistic "scroll back through recent deploys / roll back" need; the
// archive isn't a forensic log.
const buildRecordCap = 100

// ListBuildRecords returns up to `limit` archived build summaries for a
// service, newest-first by createdAt. limit<=0 (or >cap) clamps to
// buildRecordCap. Used by the Deployments list to backfill builds whose
// live CR has been GC'd.
func (d *DB) ListBuildRecords(ctx context.Context, project, service string, limit int) ([]BuildRecord, error) {
	if limit <= 0 || limit > buildRecordCap {
		limit = buildRecordCap
	}
	rows, err := d.QueryContext(ctx, `
		SELECT "buildName","project","service","branch","commitSha","commitMessage",
		       "imageTag","status","startedAt","finishedAt","triggeredBy",
		       "triggeredByUser","errorMessage","createdAt"
		  FROM "BuildRecord"
		 WHERE "project"=$1 AND "service"=$2
		 ORDER BY "createdAt" DESC
		 LIMIT $3`, project, service, limit)
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

// GetBuildImage returns the archived service short-name + image tag +
// status for one build, for the rollback fallback when the live CR is
// gone. ok=false when no record exists. The caller reconstructs the
// full image repo from "<registry>/<project>/<service>".
func (d *DB) GetBuildImage(ctx context.Context, project, buildName string) (service, tag, phase string, ok bool, err error) {
	row := d.QueryRowContext(ctx,
		`SELECT "service","imageTag","status" FROM "BuildRecord" WHERE "buildName"=$1 AND "project"=$2`,
		buildName, project)
	if scanErr := row.Scan(&service, &tag, &phase); scanErr != nil {
		if errors.Is(scanErr, sql.ErrNoRows) {
			return "", "", "", false, nil
		}
		return "", "", "", false, fmt.Errorf("GetBuildImage: %w", scanErr)
	}
	return service, tag, phase, true, nil
}

// ListArchivedImages returns one row per archived build in a project (or
// all projects when project==""), carrying the fields the image-
// retention sweep needs. Used to extend the rollback-window sweep over
// builds whose CR is already GC'd.
func (d *DB) ListArchivedImages(ctx context.Context, project string) ([]ArchivedImage, error) {
	var rows *sql.Rows
	var err error
	if project == "" {
		rows, err = d.QueryContext(ctx, `
			SELECT "buildName","project","service","imageTag","status","createdAt"
			  FROM "BuildRecord" WHERE "imageTag" <> ''`)
	} else {
		rows, err = d.QueryContext(ctx, `
			SELECT "buildName","project","service","imageTag","status","createdAt"
			  FROM "BuildRecord" WHERE "project"=$1 AND "imageTag" <> ''`, project)
	}
	if err != nil {
		return nil, fmt.Errorf("ListArchivedImages: %w", err)
	}
	defer rows.Close()
	var out []ArchivedImage
	for rows.Next() {
		var a ArchivedImage
		if err := rows.Scan(&a.BuildName, &a.Project, &a.Service, &a.ImageTag, &a.Status, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("ListArchivedImages scan: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ArchivedImage is the image-retention projection of a BuildRecord.
type ArchivedImage struct {
	BuildName string
	Project   string
	Service   string
	ImageTag  string
	Status    string
	CreatedAt time.Time
}

// ClearImageTag blanks the imageTag on a record after its registry
// image has been pruned (aged past the rollback window). The record
// stays in the Deployments list as history, but a cleared imageTag
// signals "no longer rollback-able" to the UI and the rollback handler.
func (d *DB) ClearImageTag(ctx context.Context, project, buildName string) error {
	_, err := d.ExecContext(ctx,
		`UPDATE "BuildRecord" SET "imageTag"='' WHERE "project"=$1 AND "buildName"=$2`,
		project, buildName)
	if err != nil {
		return fmt.Errorf("ClearImageTag: %w", err)
	}
	return nil
}

// DeleteBuildRecordsForService removes archived summaries for a service
// — called on service delete so history doesn't outlive the service.
// Mirrors DeleteBuildLogsForService.
func (d *DB) DeleteBuildRecordsForService(ctx context.Context, project, service string) error {
	_, err := d.ExecContext(ctx,
		`DELETE FROM "BuildRecord" WHERE "project"=$1 AND "service"=$2`,
		project, service)
	if err != nil {
		return fmt.Errorf("DeleteBuildRecordsForService: %w", err)
	}
	return nil
}
