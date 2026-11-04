package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"
)

// Vulnerability scanning persistence (docs/VULN_SCANNING.md). VulnScan
// is both the scan record and the work queue: rows start queued, the
// leader's worker claims one at a time (running), and finishes it.

const (
	VulnStatusQueued    = "queued"
	VulnStatusRunning   = "running"
	VulnStatusSucceeded = "succeeded"
	VulnStatusFailed    = "failed"

	VulnReasonGate    = "gate"
	VulnReasonNightly = "nightly"
	VulnReasonManual  = "manual"

	VulnOwnerService  = "service"
	VulnOwnerAddon    = "addon"
	VulnOwnerPlatform = "platform"
)

// VulnCounts are per-severity finding counts.
type VulnCounts struct {
	Critical        int `json:"critical"`
	High            int `json:"high"`
	Medium          int `json:"medium"`
	Low             int `json:"low"`
	Unknown         int `json:"unknown"`
	FixableCritical int `json:"fixableCritical"`
	FixableHigh     int `json:"fixableHigh"`
}

// Add counts one finding.
func (c *VulnCounts) Add(severity string, fixable bool) {
	switch severity {
	case "CRITICAL":
		c.Critical++
		if fixable {
			c.FixableCritical++
		}
	case "HIGH":
		c.High++
		if fixable {
			c.FixableHigh++
		}
	case "MEDIUM":
		c.Medium++
	case "LOW":
		c.Low++
	default:
		c.Unknown++
	}
}

// VulnScan is one scan of one image digest. Counts include every
// finding; callers that exclude ignored findings recount.
type VulnScan struct {
	ID           int64
	Digest       string
	ImageRef     string
	Status       string
	Error        string
	TrivyVersion string
	DBUpdatedAt  time.Time
	CreatedAt    time.Time
	StartedAt    time.Time
	FinishedAt   time.Time
	Counts       VulnCounts
	JobName      string
	Reason       string
	// PullSecret is "<namespace>/<name>" of a dockerconfigjson Secret
	// with credentials for the image's registry, or "".
	PullSecret string
}

// VulnFinding is one vulnerability in one package of a scanned image.
type VulnFinding struct {
	VulnID           string
	PkgName          string
	PkgType          string
	InstalledVersion string
	FixedVersion     string
	Severity         string
	Title            string
	PrimaryURL       string
	Target           string
}

// VulnImage is one inventory row: an image digest some owner runs.
type VulnImage struct {
	Digest      string
	ImageRef    string
	OwnerKind   string
	Project     string
	Service     string
	Env         string
	Addon       string
	Namespace   string
	Workload    string
	PullSecret  string
	FirstSeenAt time.Time
	LastSeenAt  time.Time
}

// VulnIgnore suppresses one vulnerability ID for one owner. Scope
// platform has empty Project/Service and covers every platform image.
// For scope addon, Service holds the addon name.
type VulnIgnore struct {
	ID        int64
	Scope     string
	Project   string
	Service   string
	VulnID    string
	Reason    string
	ExpiresAt time.Time // zero = never
	CreatedBy string
	CreatedAt time.Time
}

// Expired reports whether the ignore no longer applies at now.
func (i VulnIgnore) Expired(now time.Time) bool {
	return !i.ExpiresAt.IsZero() && !now.Before(i.ExpiresAt)
}

const vulnScanCols = `"id","digest","imageRef","status","error","trivyVersion","dbUpdatedAt","createdAt","startedAt","finishedAt",
	"critical","high","medium","low","unknown","fixableCritical","fixableHigh","jobName","reason","pullSecret"`

type rowScanner interface{ Scan(dest ...any) error }

func scanVulnScan(r rowScanner) (VulnScan, error) {
	var s VulnScan
	var dbUpdated, created, started, finished prismaTime
	err := r.Scan(&s.ID, &s.Digest, &s.ImageRef, &s.Status, &s.Error, &s.TrivyVersion,
		&dbUpdated, &created, &started, &finished,
		&s.Counts.Critical, &s.Counts.High, &s.Counts.Medium, &s.Counts.Low, &s.Counts.Unknown,
		&s.Counts.FixableCritical, &s.Counts.FixableHigh, &s.JobName, &s.Reason, &s.PullSecret)
	s.DBUpdatedAt, s.CreatedAt, s.StartedAt, s.FinishedAt = dbUpdated.Time, created.Time, started.Time, finished.Time
	return s, err
}

func (d *DB) queryVulnScans(ctx context.Context, q string, args ...any) ([]VulnScan, error) {
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VulnScan
	for rows.Next() {
		s, err := scanVulnScan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// VulnScanRequest asks for a scan. Digest may be empty when it can't be
// resolved up front; the worker fills it from Trivy's report.
type VulnScanRequest struct {
	Digest     string
	ImageRef   string
	Reason     string
	PullSecret string
}

// EnqueueVulnScan queues a scan unless one for the same image is
// already queued or running, in which case that scan's id is returned
// with created=false.
func (d *DB) EnqueueVulnScan(ctx context.Context, req VulnScanRequest) (id int64, created bool, err error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, fmt.Errorf("enqueue vuln scan: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	key := req.Digest
	if key == "" {
		key = req.ImageRef
	}
	// Serialise enqueues of one image so two callers can't both miss
	// the in-flight row and queue a duplicate.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('vulnscan:' || $1))`, key); err != nil {
		return 0, false, fmt.Errorf("enqueue vuln scan lock: %w", err)
	}
	err = tx.QueryRowContext(ctx, `
		SELECT "id" FROM "VulnScan"
		WHERE "status" IN ('queued','running')
		  AND (("digest" <> '' AND "digest" = $1) OR ("digest" = '' AND "imageRef" = $2))
		ORDER BY "id" LIMIT 1`, req.Digest, req.ImageRef).Scan(&id)
	switch {
	case err == nil:
		if req.Reason == VulnReasonGate {
			// A build is now waiting on this scan: jump the nightly queue.
			if _, err := tx.ExecContext(ctx, `UPDATE "VulnScan" SET "reason" = 'gate' WHERE "id" = $1 AND "status" = 'queued'`, id); err != nil {
				return 0, false, fmt.Errorf("enqueue vuln scan promote: %w", err)
			}
			if err := tx.Commit(); err != nil {
				return 0, false, fmt.Errorf("enqueue vuln scan commit: %w", err)
			}
		}
		return id, false, nil
	case !errors.Is(err, sql.ErrNoRows):
		return 0, false, fmt.Errorf("enqueue vuln scan lookup: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO "VulnScan" ("digest","imageRef","status","reason","pullSecret")
		VALUES ($1,$2,'queued',$3,$4) RETURNING "id"`,
		req.Digest, req.ImageRef, req.Reason, req.PullSecret).Scan(&id); err != nil {
		return 0, false, fmt.Errorf("enqueue vuln scan insert: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, false, fmt.Errorf("enqueue vuln scan commit: %w", err)
	}
	return id, true, nil
}

// GetVulnScan returns one scan; ErrNotFound when absent.
func (d *DB) GetVulnScan(ctx context.Context, id int64) (VulnScan, error) {
	s, err := scanVulnScan(d.QueryRowContext(ctx, `SELECT `+vulnScanCols+` FROM "VulnScan" WHERE "id" = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return VulnScan{}, fmt.Errorf("%w: vuln scan %d", ErrNotFound, id)
	}
	if err != nil {
		return VulnScan{}, fmt.Errorf("get vuln scan: %w", err)
	}
	return s, nil
}

// LatestVulnScans returns the newest scan (any status) per digest.
func (d *DB) LatestVulnScans(ctx context.Context, digests []string) (map[string]VulnScan, error) {
	return d.latestVulnScans(ctx, digests, "")
}

// LatestSucceededVulnScans returns the newest succeeded scan per digest.
func (d *DB) LatestSucceededVulnScans(ctx context.Context, digests []string) (map[string]VulnScan, error) {
	return d.latestVulnScans(ctx, digests, VulnStatusSucceeded)
}

func (d *DB) latestVulnScans(ctx context.Context, digests []string, status string) (map[string]VulnScan, error) {
	out := map[string]VulnScan{}
	if len(digests) == 0 {
		return out, nil
	}
	scans, err := d.queryVulnScans(ctx, `
		SELECT DISTINCT ON ("digest") `+vulnScanCols+` FROM "VulnScan"
		WHERE "digest" = ANY($1) AND ($2 = '' OR "status" = $2)
		ORDER BY "digest", "createdAt" DESC, "id" DESC`, pq.Array(digests), status)
	if err != nil {
		return nil, fmt.Errorf("latest vuln scans: %w", err)
	}
	for _, s := range scans {
		out[s.Digest] = s
	}
	return out, nil
}

// LatestVulnScanForImage returns the newest scan of digest, or — when
// digest is empty — of imageRef with no digest yet. nil when none.
func (d *DB) LatestVulnScanForImage(ctx context.Context, digest, imageRef string) (*VulnScan, error) {
	scans, err := d.queryVulnScans(ctx, `
		SELECT `+vulnScanCols+` FROM "VulnScan"
		WHERE ($1 <> '' AND "digest" = $1) OR ($1 = '' AND "imageRef" = $2)
		ORDER BY "createdAt" DESC, "id" DESC LIMIT 1`, digest, imageRef)
	if err != nil {
		return nil, fmt.Errorf("latest vuln scan: %w", err)
	}
	if len(scans) == 0 {
		return nil, nil
	}
	return &scans[0], nil
}

// PreviousFinishedVulnScan returns the newest finished scan of digest
// older than beforeID, or nil.
func (d *DB) PreviousFinishedVulnScan(ctx context.Context, digest string, beforeID int64) (*VulnScan, error) {
	scans, err := d.queryVulnScans(ctx, `
		SELECT `+vulnScanCols+` FROM "VulnScan"
		WHERE "digest" = $1 AND "id" < $2 AND "status" IN ('succeeded','failed')
		ORDER BY "id" DESC LIMIT 1`, digest, beforeID)
	if err != nil {
		return nil, fmt.Errorf("previous vuln scan: %w", err)
	}
	if len(scans) == 0 {
		return nil, nil
	}
	return &scans[0], nil
}

// ListRunningVulnScans returns scans the worker has claimed.
func (d *DB) ListRunningVulnScans(ctx context.Context) ([]VulnScan, error) {
	scans, err := d.queryVulnScans(ctx, `SELECT `+vulnScanCols+` FROM "VulnScan" WHERE "status" = 'running' ORDER BY "id"`)
	if err != nil {
		return nil, fmt.Errorf("list running vuln scans: %w", err)
	}
	return scans, nil
}

// CountActiveVulnScans counts queued + running scans among digests.
func (d *DB) CountActiveVulnScans(ctx context.Context, digests []string) (int, error) {
	if len(digests) == 0 {
		return 0, nil
	}
	var n int
	if err := d.QueryRowContext(ctx, `
		SELECT count(*) FROM "VulnScan" WHERE "status" IN ('queued','running') AND "digest" = ANY($1)`,
		pq.Array(digests)).Scan(&n); err != nil {
		return 0, fmt.Errorf("count active vuln scans: %w", err)
	}
	return n, nil
}

// ClaimNextVulnScan moves the next queued scan to running and returns
// it, or nil when the queue is empty or a scan is already running
// (global concurrency is one). Gate scans go first: a build is waiting.
func (d *DB) ClaimNextVulnScan(ctx context.Context) (*VulnScan, error) {
	scans, err := d.queryVulnScans(ctx, `
		UPDATE "VulnScan" SET "status" = 'running', "startedAt" = now()
		WHERE "id" = (
			SELECT "id" FROM "VulnScan" WHERE "status" = 'queued'
			ORDER BY CASE "reason" WHEN 'gate' THEN 0 WHEN 'manual' THEN 1 ELSE 2 END, "id"
			LIMIT 1 FOR UPDATE SKIP LOCKED)
		AND NOT EXISTS (SELECT 1 FROM "VulnScan" WHERE "status" = 'running')
		RETURNING `+vulnScanCols)
	if IsUniqueViolation(err) {
		return nil, nil // another claim won the race for the one running slot
	}
	if err != nil {
		return nil, fmt.Errorf("claim vuln scan: %w", err)
	}
	if len(scans) == 0 {
		return nil, nil
	}
	return &scans[0], nil
}

// SetVulnScanJob records the Job running a scan.
func (d *DB) SetVulnScanJob(ctx context.Context, id int64, jobName string) error {
	if _, err := d.ExecContext(ctx, `UPDATE "VulnScan" SET "jobName" = $2 WHERE "id" = $1`, id, jobName); err != nil {
		return fmt.Errorf("set vuln scan job: %w", err)
	}
	return nil
}

// VulnScanResult is a finished scan's payload.
type VulnScanResult struct {
	// Digest fills in a scan queued without one; ignored otherwise.
	Digest       string
	TrivyVersion string
	DBUpdatedAt  time.Time
	Findings     []VulnFinding
}

// CompleteVulnScan stores findings and marks a running scan succeeded.
// A scan that is no longer running (failed by a timeout meanwhile) is
// left alone.
func (d *DB) CompleteVulnScan(ctx context.Context, id int64, res VulnScanResult) error {
	var c VulnCounts
	for _, f := range res.Findings {
		c.Add(f.Severity, f.FixedVersion != "")
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("complete vuln scan: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	r, err := tx.ExecContext(ctx, `
		UPDATE "VulnScan" SET "status" = 'succeeded', "error" = '', "finishedAt" = now(),
			"digest" = CASE WHEN "digest" = '' THEN $2 ELSE "digest" END,
			"trivyVersion" = $3, "dbUpdatedAt" = $4,
			"critical" = $5, "high" = $6, "medium" = $7, "low" = $8, "unknown" = $9,
			"fixableCritical" = $10, "fixableHigh" = $11
		WHERE "id" = $1 AND "status" = 'running'`,
		id, res.Digest, res.TrivyVersion, prismaTime{res.DBUpdatedAt},
		c.Critical, c.High, c.Medium, c.Low, c.Unknown, c.FixableCritical, c.FixableHigh)
	if err != nil {
		return fmt.Errorf("complete vuln scan: %w", err)
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: running vuln scan %d", ErrNotFound, id)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM "VulnFinding" WHERE "scanId" = $1`, id); err != nil {
		return fmt.Errorf("complete vuln scan: clear findings: %w", err)
	}
	if len(res.Findings) > 0 {
		stmt, err := tx.PrepareContext(ctx, pq.CopyIn("VulnFinding", "scanId", "vulnId", "pkgName", "pkgType",
			"installedVersion", "fixedVersion", "severity", "title", "primaryURL", "target"))
		if err != nil {
			return fmt.Errorf("complete vuln scan: prepare copy: %w", err)
		}
		for _, f := range res.Findings {
			if _, err := stmt.ExecContext(ctx, id, f.VulnID, f.PkgName, f.PkgType, f.InstalledVersion,
				f.FixedVersion, f.Severity, f.Title, f.PrimaryURL, f.Target); err != nil {
				_ = stmt.Close()
				return fmt.Errorf("complete vuln scan: copy finding: %w", err)
			}
		}
		if _, err := stmt.ExecContext(ctx); err != nil {
			_ = stmt.Close()
			return fmt.Errorf("complete vuln scan: flush findings: %w", err)
		}
		if err := stmt.Close(); err != nil {
			return fmt.Errorf("complete vuln scan: close copy: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("complete vuln scan commit: %w", err)
	}
	return nil
}

// FailVulnScan marks a queued or running scan failed.
func (d *DB) FailVulnScan(ctx context.Context, id int64, msg string) error {
	if _, err := d.ExecContext(ctx, `
		UPDATE "VulnScan" SET "status" = 'failed', "error" = $2, "finishedAt" = now()
		WHERE "id" = $1 AND "status" IN ('queued','running')`, id, msg); err != nil {
		return fmt.Errorf("fail vuln scan: %w", err)
	}
	return nil
}

// ListVulnFindings returns a scan's findings, most severe first.
func (d *DB) ListVulnFindings(ctx context.Context, scanID int64) ([]VulnFinding, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT "vulnId","pkgName","pkgType","installedVersion","fixedVersion","severity","title","primaryURL","target"
		FROM "VulnFinding" WHERE "scanId" = $1
		ORDER BY CASE "severity" WHEN 'CRITICAL' THEN 0 WHEN 'HIGH' THEN 1 WHEN 'MEDIUM' THEN 2 WHEN 'LOW' THEN 3 ELSE 4 END,
			"vulnId", "pkgName"`, scanID)
	if err != nil {
		return nil, fmt.Errorf("list vuln findings: %w", err)
	}
	defer rows.Close()
	out := []VulnFinding{}
	for rows.Next() {
		var f VulnFinding
		if err := rows.Scan(&f.VulnID, &f.PkgName, &f.PkgType, &f.InstalledVersion, &f.FixedVersion,
			&f.Severity, &f.Title, &f.PrimaryURL, &f.Target); err != nil {
			return nil, fmt.Errorf("scan vuln finding: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// UpsertVulnImages records inventory rows as seen at seen.
func (d *DB) UpsertVulnImages(ctx context.Context, imgs []VulnImage, seen time.Time) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("upsert vuln images: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, im := range imgs {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO "VulnImage" ("digest","imageRef","ownerKind","project","service","env","addon","namespace","workload","pullSecret","firstSeenAt","lastSeenAt")
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11)
			ON CONFLICT ("digest","ownerKind","project","service","env","addon","namespace","workload") DO UPDATE SET
				"imageRef" = EXCLUDED."imageRef", "pullSecret" = EXCLUDED."pullSecret",
				"lastSeenAt" = GREATEST("VulnImage"."lastSeenAt", EXCLUDED."lastSeenAt")`,
			im.Digest, im.ImageRef, im.OwnerKind, im.Project, im.Service, im.Env, im.Addon,
			im.Namespace, im.Workload, im.PullSecret, seen.UTC()); err != nil {
			return fmt.Errorf("upsert vuln image %s: %w", im.Digest, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("upsert vuln images commit: %w", err)
	}
	return nil
}

// VulnImageFilter selects inventory rows. Empty fields don't filter.
type VulnImageFilter struct {
	OwnerKind string
	Project   string
	Service   string
	Env       string
	Addon     string
	Digest    string
	// SeenSince drops rows last seen before it ("gone" images).
	SeenSince time.Time
}

// ListVulnImages returns inventory rows, newest-seen first.
func (d *DB) ListVulnImages(ctx context.Context, f VulnImageFilter) ([]VulnImage, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT "digest","imageRef","ownerKind","project","service","env","addon","namespace","workload","pullSecret","firstSeenAt","lastSeenAt"
		FROM "VulnImage"
		WHERE ($1 = '' OR "ownerKind" = $1) AND ($2 = '' OR "project" = $2) AND ($3 = '' OR "service" = $3)
		  AND ($4 = '' OR "env" = $4) AND ($5 = '' OR "addon" = $5) AND ($6 = '' OR "digest" = $6)
		  AND "lastSeenAt" >= $7
		ORDER BY "lastSeenAt" DESC, "ownerKind", "project", "service", "addon", "namespace", "workload"`,
		f.OwnerKind, f.Project, f.Service, f.Env, f.Addon, f.Digest, f.SeenSince.UTC())
	if err != nil {
		return nil, fmt.Errorf("list vuln images: %w", err)
	}
	defer rows.Close()
	out := []VulnImage{}
	for rows.Next() {
		var im VulnImage
		var first, last prismaTime
		if err := rows.Scan(&im.Digest, &im.ImageRef, &im.OwnerKind, &im.Project, &im.Service, &im.Env,
			&im.Addon, &im.Namespace, &im.Workload, &im.PullSecret, &first, &last); err != nil {
			return nil, fmt.Errorf("scan vuln image: %w", err)
		}
		im.FirstSeenAt, im.LastSeenAt = first.Time, last.Time
		out = append(out, im)
	}
	return out, rows.Err()
}

// GetVulnPolicy returns a service's policy mode; "inherit" when unset.
func (d *DB) GetVulnPolicy(ctx context.Context, project, service string) (string, error) {
	var mode string
	err := d.QueryRowContext(ctx, `SELECT "mode" FROM "VulnPolicy" WHERE "project" = $1 AND "service" = $2`,
		project, service).Scan(&mode)
	if errors.Is(err, sql.ErrNoRows) {
		return "inherit", nil
	}
	if err != nil {
		return "", fmt.Errorf("get vuln policy: %w", err)
	}
	return mode, nil
}

// ListVulnPolicies returns every non-inherit policy keyed "project/service".
func (d *DB) ListVulnPolicies(ctx context.Context) (map[string]string, error) {
	rows, err := d.QueryContext(ctx, `SELECT "project","service","mode" FROM "VulnPolicy"`)
	if err != nil {
		return nil, fmt.Errorf("list vuln policies: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var p, s, m string
		if err := rows.Scan(&p, &s, &m); err != nil {
			return nil, fmt.Errorf("scan vuln policy: %w", err)
		}
		out[p+"/"+s] = m
	}
	return out, rows.Err()
}

// SetVulnPolicy stores a service's policy; "inherit" deletes the row.
func (d *DB) SetVulnPolicy(ctx context.Context, project, service, mode string) error {
	var err error
	if mode == "inherit" {
		_, err = d.ExecContext(ctx, `DELETE FROM "VulnPolicy" WHERE "project" = $1 AND "service" = $2`, project, service)
	} else {
		_, err = d.ExecContext(ctx, `
			INSERT INTO "VulnPolicy" ("project","service","mode","updatedAt") VALUES ($1,$2,$3,now())
			ON CONFLICT ("project","service") DO UPDATE SET "mode" = EXCLUDED."mode", "updatedAt" = now()`,
			project, service, mode)
	}
	if err != nil {
		return fmt.Errorf("set vuln policy: %w", err)
	}
	return nil
}

const vulnIgnoreCols = `"id","scope","project","service","vulnId","reason","expiresAt","createdBy","createdAt"`

// ListVulnIgnores returns one owner's ignores (expired included),
// newest first.
func (d *DB) ListVulnIgnores(ctx context.Context, scope, project, service string) ([]VulnIgnore, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+vulnIgnoreCols+` FROM "VulnIgnore"
		WHERE "scope" = $1 AND "project" = $2 AND "service" = $3 ORDER BY "createdAt" DESC, "id" DESC`,
		scope, project, service)
	if err != nil {
		return nil, fmt.Errorf("list vuln ignores: %w", err)
	}
	defer rows.Close()
	out := []VulnIgnore{}
	for rows.Next() {
		var ig VulnIgnore
		var exp, created prismaTime
		if err := rows.Scan(&ig.ID, &ig.Scope, &ig.Project, &ig.Service, &ig.VulnID, &ig.Reason,
			&exp, &ig.CreatedBy, &created); err != nil {
			return nil, fmt.Errorf("scan vuln ignore: %w", err)
		}
		ig.ExpiresAt, ig.CreatedAt = exp.Time, created.Time
		out = append(out, ig)
	}
	return out, rows.Err()
}

// CreateVulnIgnore inserts an ignore and returns it with id + createdAt.
func (d *DB) CreateVulnIgnore(ctx context.Context, ig VulnIgnore) (VulnIgnore, error) {
	var created prismaTime
	if err := d.QueryRowContext(ctx, `
		INSERT INTO "VulnIgnore" ("scope","project","service","vulnId","reason","expiresAt","createdBy")
		VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING "id","createdAt"`,
		ig.Scope, ig.Project, ig.Service, ig.VulnID, ig.Reason, prismaTime{ig.ExpiresAt}, ig.CreatedBy,
	).Scan(&ig.ID, &created); err != nil {
		return VulnIgnore{}, fmt.Errorf("create vuln ignore: %w", err)
	}
	ig.CreatedAt = created.Time
	return ig, nil
}

// DeleteVulnIgnore deletes an ignore only if it belongs to the given
// owner, so an id from another project can't be deleted through this
// one. ErrNotFound when nothing matched.
func (d *DB) DeleteVulnIgnore(ctx context.Context, id int64, scope, project, service string) error {
	r, err := d.ExecContext(ctx, `DELETE FROM "VulnIgnore" WHERE "id" = $1 AND "scope" = $2 AND "project" = $3 AND "service" = $4`,
		id, scope, project, service)
	if err != nil {
		return fmt.Errorf("delete vuln ignore: %w", err)
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: vuln ignore %d", ErrNotFound, id)
	}
	return nil
}

// UnnotifiedVulnIDs returns the ids in vulnIDs not yet notified for owner.
func (d *DB) UnnotifiedVulnIDs(ctx context.Context, ownerKey string, vulnIDs []string) ([]string, error) {
	if len(vulnIDs) == 0 {
		return nil, nil
	}
	rows, err := d.QueryContext(ctx, `
		SELECT v FROM unnest($2::text[]) AS v
		WHERE NOT EXISTS (SELECT 1 FROM "VulnNotified" n WHERE n."ownerKey" = $1 AND n."vulnId" = v)`,
		ownerKey, pq.Array(vulnIDs))
	if err != nil {
		return nil, fmt.Errorf("unnotified vulns: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("scan unnotified vuln: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// MarkVulnsNotified records (owner, vulnId) pairs as notified.
func (d *DB) MarkVulnsNotified(ctx context.Context, ownerKey string, vulnIDs []string) error {
	if len(vulnIDs) == 0 {
		return nil
	}
	if _, err := d.ExecContext(ctx, `
		INSERT INTO "VulnNotified" ("ownerKey","vulnId") SELECT $1, v FROM unnest($2::text[]) AS v
		ON CONFLICT DO NOTHING`, ownerKey, pq.Array(vulnIDs)); err != nil {
		return fmt.Errorf("mark vulns notified: %w", err)
	}
	return nil
}

// PruneVulnScans applies retention: keep the newest keepPerDigest
// finished scans per digest, and drop finished scans older than
// goneAfter whose digest no inventory row has seen since then. Queued
// and running scans are never pruned. Findings cascade.
func (d *DB) PruneVulnScans(ctx context.Context, now time.Time, keepPerDigest int, goneAfter time.Duration) (int64, error) {
	cutoff := now.Add(-goneAfter).UTC()
	r, err := d.ExecContext(ctx, `
		DELETE FROM "VulnScan" s
		WHERE s."status" IN ('succeeded','failed') AND (
			s."id" IN (
				SELECT "id" FROM (
					SELECT "id", row_number() OVER (PARTITION BY "digest" ORDER BY "createdAt" DESC, "id" DESC) AS rn
					FROM "VulnScan" WHERE "status" IN ('succeeded','failed') AND "digest" <> ''
				) ranked WHERE rn > $1)
			OR (s."createdAt" < $2 AND NOT EXISTS (
				SELECT 1 FROM "VulnImage" i WHERE i."digest" = s."digest" AND i."lastSeenAt" >= $2)))`,
		keepPerDigest, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune vuln scans: %w", err)
	}
	n, _ := r.RowsAffected()
	return n, nil
}

// VulnScanSettings is the instance-wide config (Setting keys vulnscan.*).
// The JSON shape is the HTTP contract.
type VulnScanSettings struct {
	Mode              string `json:"mode"`
	ScanAddons        bool   `json:"scanAddons"`
	ScanPlatform      bool   `json:"scanPlatform"`
	NightlyHourUTC    int    `json:"nightlyHourUTC"`
	NotifyMinSeverity string `json:"notifyMinSeverity"`
}

// DefaultVulnScanSettings is a fresh install's config.
func DefaultVulnScanSettings() VulnScanSettings {
	return VulnScanSettings{Mode: "enforce", ScanAddons: true, ScanPlatform: true, NightlyHourUTC: 3, NotifyMinSeverity: "high"}
}

// GetVulnScanSettings reads vulnscan.* over the defaults.
func (d *DB) GetVulnScanSettings(ctx context.Context) (VulnScanSettings, error) {
	out := DefaultVulnScanSettings()
	rows, err := d.QueryContext(ctx, `SELECT key, value FROM "Setting" WHERE key LIKE 'vulnscan.%'`)
	if err != nil {
		return out, fmt.Errorf("get vulnscan settings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return out, fmt.Errorf("scan setting: %w", err)
		}
		switch k {
		case "vulnscan.mode":
			if s := unquote(v); s != "" {
				out.Mode = s
			}
		case "vulnscan.scanAddons":
			if b, err := strconv.ParseBool(v); err == nil {
				out.ScanAddons = b
			}
		case "vulnscan.scanPlatform":
			if b, err := strconv.ParseBool(v); err == nil {
				out.ScanPlatform = b
			}
		case "vulnscan.nightlyHourUTC":
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				out.NightlyHourUTC = n
			}
		case "vulnscan.notifyMinSeverity":
			if s := unquote(v); s != "" {
				out.NotifyMinSeverity = s
			}
		}
	}
	return out, rows.Err()
}

// SetVulnScanSettings writes every vulnscan.* key. Callers validate.
func (d *DB) SetVulnScanSettings(ctx context.Context, in VulnScanSettings, updatedBy string) error {
	pairs := []struct{ key, value string }{
		{"vulnscan.mode", quote(in.Mode)},
		{"vulnscan.scanAddons", strconv.FormatBool(in.ScanAddons)},
		{"vulnscan.scanPlatform", strconv.FormatBool(in.ScanPlatform)},
		{"vulnscan.nightlyHourUTC", strconv.Itoa(in.NightlyHourUTC)},
		{"vulnscan.notifyMinSeverity", quote(in.NotifyMinSeverity)},
	}
	for _, p := range pairs {
		if err := d.SetSetting(ctx, p.key, p.value, updatedBy); err != nil {
			return fmt.Errorf("set vulnscan settings: %w", err)
		}
	}
	return nil
}
