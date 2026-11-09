// ErrorEvent is the row written by the Sentry-style error scanner.
// Every occurrence of an error line in a pod log lands here; the
// API endpoint groups by fingerprint to show "this error happened
// 47 times since 09:00, last seen 12:31" without scanning the raw
// log every request.

package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrorEvent is one occurrence of a captured error.
type ErrorEvent struct {
	ID          int64
	Project     string
	Service     string
	Env         string
	Pod         string
	Fingerprint string
	Message     string
	RawLine     string
	Ts          time.Time
	CreatedAt   time.Time
}

// ErrorGroup is the aggregated shape returned to the UI.
type ErrorGroup struct {
	Fingerprint string    `json:"fingerprint"`
	Message     string    `json:"message"`
	Count       int64     `json:"count"`
	FirstSeen   time.Time `json:"firstSeen"`
	LastSeen    time.Time `json:"lastSeen"`
	SampleLine  string    `json:"sampleLine"`
	SampleEnv   string    `json:"sampleEnv,omitempty"`
	SamplePod   string    `json:"samplePod,omitempty"`
	// ResolvedAt is set by the errors handler when someone resolved the
	// group; not a column.
	ResolvedAt *time.Time `json:"resolvedAt,omitempty"`
}

// Build and release-hook output that the error scanner stored before it
// skipped non-runtime pods (see errorscan/source.go). Every reader below
// excludes rows matching either pattern, passed as query parameters. Both
// are valid Go RE2 and Postgres ARE.
const (
	// BuildkitStepPattern matches buildctl's plain progress output
	// ("#5 ERROR: …"), which no runtime logger prints.
	BuildkitStepPattern = `^#[0-9]+ `
	// ReleasePodPattern matches a release-hook Job pod name,
	// <env>-release-<tag>-<sha256[:8]>-<pod suffix>.
	ReleasePodPattern = `-release-[a-z0-9-]+-[0-9a-f]{8}-[a-z0-9]{5}$`
)

// hideSplitContinuation drops rows the error scanner wrote before it
// grouped multi-line errors: a continuation line (indented frame, "at …")
// or a sibling line from the same write ("… line one" / "… line two")
// that became its own group, or a bare "Traceback (most recent call
// last):" header whose exception line went into another row. A row is
// such a split when an earlier row from the same pod, with another fingerprint, sits just before it. The
// sibling test mirrors errorscan's rule (same millisecond, a shared
// prefix of at least 16 bytes and half the shorter first line). Every
// reader below applies it to the row aliased e, so counts agree.
const hideSplitContinuation = `NOT EXISTS (
		SELECT 1 FROM "ErrorEvent" sib
		 WHERE sib.project = e.project AND sib.service = e.service AND sib.pod = e.pod
		   AND sib.ts < e.ts AND sib.ts >= e.ts - interval '100 milliseconds'
		   AND sib.fingerprint <> e.fingerprint
		   AND (e."rawLine" ~ '^(\s|at\s|Traceback \(most recent call last\):?\s*$)'
		        OR (sib.ts >= e.ts - interval '1 millisecond'
		            AND length(split_part(e."rawLine", E'\n', 1)) >= 16
		            AND left(sib."rawLine", greatest(16, least(length(split_part(sib."rawLine", E'\n', 1)), length(split_part(e."rawLine", E'\n', 1))) / 2))
		              = left(e."rawLine", greatest(16, least(length(split_part(sib."rawLine", E'\n', 1)), length(split_part(e."rawLine", E'\n', 1))) / 2)))))`

// InsertErrorEvent appends an occurrence row.
func (d *DB) InsertErrorEvent(ctx context.Context, e ErrorEvent) error {
	_, err := d.ExecContext(ctx, `
		INSERT INTO "ErrorEvent"
		    ("project","service","env","pod","fingerprint","message","rawLine","ts")
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		e.Project, e.Service, e.Env, e.Pod, e.Fingerprint, e.Message, e.RawLine, e.Ts.UTC(),
	)
	if err != nil {
		return fmt.Errorf("InsertErrorEvent: %w", err)
	}
	return nil
}

// ListErrorGroups returns aggregated groups for (project, service)
// within the lookback window. Newest-first by lastSeen. offset skips
// that many groups (offset paging for the errors endpoint); negative
// offsets are treated as 0.
//
// The aggregation runs server-side to keep the wire payload small;
// a chatty 1000-error-per-minute service would otherwise return
// 60k rows for a 1-hour view.
//
// The limit clamp allows up to 201 (not 200): the HTTP handler
// over-fetches one row past its own 200-cap to detect truncation
// exactly instead of guessing from a full page.
// env, when non-empty, restricts to one environment (the pod's
// kuso.sislelabs.com/env label, e.g. "production") before grouping, so a
// group's count and sample come from that env only.
func (d *DB) ListErrorGroups(ctx context.Context, project, service, env string, since time.Time, limit, offset int) ([]ErrorGroup, error) {
	if limit <= 0 || limit > 201 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := d.QueryContext(ctx, `
		SELECT fingerprint,
		       MIN(message) AS message,
		       COUNT(*) AS occurrences,
		       MIN(ts) AS first_seen,
		       MAX(ts) AS last_seen,
		       (SELECT "rawLine" FROM "ErrorEvent" e2
		         WHERE e2.project = e.project
		           AND e2.service = e.service
		           AND e2.fingerprint = e.fingerprint
		           AND ($6 = '' OR e2.env = $6)
		           AND NOT (e2."rawLine" ~ $7 OR e2.pod ~ $8)
		         ORDER BY ts DESC LIMIT 1) AS sample_line,
		       (SELECT env FROM "ErrorEvent" e3
		         WHERE e3.project = e.project
		           AND e3.service = e.service
		           AND e3.fingerprint = e.fingerprint
		           AND ($6 = '' OR e3.env = $6)
		           AND NOT (e3."rawLine" ~ $7 OR e3.pod ~ $8)
		         ORDER BY ts DESC LIMIT 1) AS sample_env,
		       (SELECT pod FROM "ErrorEvent" e4
		         WHERE e4.project = e.project
		           AND e4.service = e.service
		           AND e4.fingerprint = e.fingerprint
		           AND ($6 = '' OR e4.env = $6)
		           AND NOT (e4."rawLine" ~ $7 OR e4.pod ~ $8)
		         ORDER BY ts DESC LIMIT 1) AS sample_pod
		FROM "ErrorEvent" e
		WHERE project = $1 AND service = $2 AND ts >= $3
		  AND ($6 = '' OR env = $6)
		  AND NOT (e."rawLine" ~ $7 OR e.pod ~ $8)
		  AND `+hideSplitContinuation+`
		GROUP BY project, service, fingerprint
		ORDER BY last_seen DESC
		LIMIT $4 OFFSET $5`,
		project, service, since.UTC(), limit, offset, env, BuildkitStepPattern, ReleasePodPattern,
	)
	if err != nil {
		return nil, fmt.Errorf("ListErrorGroups: %w", err)
	}
	defer rows.Close()
	out := []ErrorGroup{}
	for rows.Next() {
		var g ErrorGroup
		var first, last sql.NullTime
		if err := rows.Scan(&g.Fingerprint, &g.Message, &g.Count, &first, &last,
			&g.SampleLine, &g.SampleEnv, &g.SamplePod); err != nil {
			return nil, fmt.Errorf("scan ErrorGroup: %w", err)
		}
		if first.Valid {
			g.FirstSeen = first.Time
		}
		if last.Valid {
			g.LastSeen = last.Time
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ScannerWatermark returns the last LogLine.id processed by the
// error scanner. Returns 0 + nil error when the scanner hasn't run
// yet (so the first tick scans from id 0 forward, picking up the
// freshest few hundred lines).
func (d *DB) ScannerWatermark(ctx context.Context, key string) (int64, error) {
	var v int64
	err := d.QueryRowContext(ctx,
		`SELECT value FROM "ErrorScannerState" WHERE key = $1`, key,
	).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("ScannerWatermark: %w", err)
	}
	return v, nil
}

// SaveScannerWatermark upserts the watermark.
func (d *DB) SaveScannerWatermark(ctx context.Context, key string, value int64) error {
	_, err := d.ExecContext(ctx, `
		INSERT INTO "ErrorScannerState" (key, value, "updatedAt")
		VALUES ($1, $2, $3)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, "updatedAt" = EXCLUDED."updatedAt"`,
		key, value, time.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("SaveScannerWatermark: %w", err)
	}
	return nil
}

// PruneErrorEvents drops rows older than `before`. Called from the
// daily cleanup goroutine.
func (d *DB) PruneErrorEvents(ctx context.Context, before time.Time) (int, error) {
	res, err := d.ExecContext(ctx, `DELETE FROM "ErrorEvent" WHERE ts < $1`, before.UTC())
	if err != nil {
		return 0, fmt.Errorf("PruneErrorEvents: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// ErrorOccurrence is one captured event of a fingerprint.
type ErrorOccurrence struct {
	Ts      time.Time `json:"ts"`
	Env     string    `json:"env,omitempty"`
	Pod     string    `json:"pod,omitempty"`
	RawLine string    `json:"rawLine"`
}

// ErrorGroupDetail backs the expanded row in the Errors tab: a count
// histogram over the window plus the newest occurrences.
type ErrorGroupDetail struct {
	Fingerprint   string            `json:"fingerprint"`
	BucketSeconds int64             `json:"bucketSeconds"`
	Buckets       [][2]int64        `json:"buckets"` // [unixSeconds, count], only non-empty buckets
	Occurrences   []ErrorOccurrence `json:"occurrences"`
}

// GetErrorGroupDetail returns the histogram + latest `limit` occurrences
// of one fingerprint since `since`. bucket is clamped to >= 60s. Uses
// the (project, service, fingerprint) index.
func (d *DB) GetErrorGroupDetail(ctx context.Context, project, service, env, fingerprint string, since time.Time, bucket time.Duration, limit int) (*ErrorGroupDetail, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	b := int64(bucket / time.Second)
	if b < 60 {
		b = 60
	}
	out := &ErrorGroupDetail{Fingerprint: fingerprint, BucketSeconds: b, Buckets: [][2]int64{}, Occurrences: []ErrorOccurrence{}}

	rows, err := d.QueryContext(ctx, `
		SELECT (floor(extract(epoch FROM ts) / $6) * $6)::bigint AS bucket, COUNT(*)
		FROM "ErrorEvent" e
		WHERE project = $1 AND service = $2 AND fingerprint = $3 AND ts >= $4
		  AND ($5 = '' OR env = $5)
		  AND NOT ("rawLine" ~ $7 OR pod ~ $8)
		  AND `+hideSplitContinuation+`
		GROUP BY bucket
		ORDER BY bucket`,
		project, service, fingerprint, since.UTC(), env, b, BuildkitStepPattern, ReleasePodPattern,
	)
	if err != nil {
		return nil, fmt.Errorf("GetErrorGroupDetail buckets: %w", err)
	}
	for rows.Next() {
		var p [2]int64
		if err := rows.Scan(&p[0], &p[1]); err != nil {
			rows.Close()
			return nil, fmt.Errorf("GetErrorGroupDetail scan bucket: %w", err)
		}
		out.Buckets = append(out.Buckets, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("GetErrorGroupDetail buckets: %w", err)
	}

	occ, err := d.QueryContext(ctx, `
		SELECT ts, env, pod, "rawLine"
		FROM "ErrorEvent" e
		WHERE project = $1 AND service = $2 AND fingerprint = $3 AND ts >= $4
		  AND ($5 = '' OR env = $5)
		  AND NOT ("rawLine" ~ $7 OR pod ~ $8)
		  AND `+hideSplitContinuation+`
		ORDER BY ts DESC
		LIMIT $6`,
		project, service, fingerprint, since.UTC(), env, limit, BuildkitStepPattern, ReleasePodPattern,
	)
	if err != nil {
		return nil, fmt.Errorf("GetErrorGroupDetail occurrences: %w", err)
	}
	defer occ.Close()
	for occ.Next() {
		var o ErrorOccurrence
		if err := occ.Scan(&o.Ts, &o.Env, &o.Pod, &o.RawLine); err != nil {
			return nil, fmt.Errorf("GetErrorGroupDetail scan occurrence: %w", err)
		}
		out.Occurrences = append(out.Occurrences, o)
	}
	return out, occ.Err()
}

// ServiceErrorSummary is one service's error rollup over a window.
type ServiceErrorSummary struct {
	Service     string    `json:"service"`
	Occurrences int64     `json:"occurrences"`
	Groups      int64     `json:"groups"`
	LastSeen    time.Time `json:"lastSeen"`
}

// SummarizeProjectErrors returns per-service error counts for a whole
// project in one query, so the canvas doesn't fan out one ListErrorGroups
// call per service. Service is the stored FQ `<project>-<service>` name.
// Services with no errors in the window are absent. env, when non-empty,
// restricts to one environment like ListErrorGroups.
func (d *DB) SummarizeProjectErrors(ctx context.Context, project, env string, since time.Time) ([]ServiceErrorSummary, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT service, COUNT(*), COUNT(DISTINCT fingerprint), MAX(ts)
		FROM "ErrorEvent" e
		WHERE project = $1 AND service <> '' AND ts >= $2 AND ($3 = '' OR env = $3)
		  AND NOT ("rawLine" ~ $4 OR pod ~ $5)
		  AND `+hideSplitContinuation+`
		GROUP BY service
		ORDER BY service`,
		project, since.UTC(), env, BuildkitStepPattern, ReleasePodPattern,
	)
	if err != nil {
		return nil, fmt.Errorf("SummarizeProjectErrors: %w", err)
	}
	defer rows.Close()
	out := []ServiceErrorSummary{}
	for rows.Next() {
		var s ServiceErrorSummary
		var last sql.NullTime
		if err := rows.Scan(&s.Service, &s.Occurrences, &s.Groups, &last); err != nil {
			return nil, fmt.Errorf("scan ServiceErrorSummary: %w", err)
		}
		if last.Valid {
			s.LastSeen = last.Time
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ErrorEventKey is the slice of an ErrorEvent the fingerprint rewrite reads.
type ErrorEventKey struct {
	ID          int64
	Fingerprint string
	Message     string
	RawLine     string
}

// MaxErrorEventID is the newest ErrorEvent id (0 when empty).
func (d *DB) MaxErrorEventID(ctx context.Context) (int64, error) {
	var id int64
	if err := d.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM "ErrorEvent"`).Scan(&id); err != nil {
		return 0, fmt.Errorf("MaxErrorEventID: %w", err)
	}
	return id, nil
}

// ListErrorEventKeys pages ErrorEvent rows with afterID < id <= upTo.
func (d *DB) ListErrorEventKeys(ctx context.Context, afterID, upTo int64, limit int) ([]ErrorEventKey, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT id, fingerprint, message, "rawLine"
		FROM "ErrorEvent"
		WHERE id > $1 AND id <= $2
		ORDER BY id
		LIMIT $3`, afterID, upTo, limit)
	if err != nil {
		return nil, fmt.Errorf("ListErrorEventKeys: %w", err)
	}
	defer rows.Close()
	out := []ErrorEventKey{}
	for rows.Next() {
		var k ErrorEventKey
		if err := rows.Scan(&k.ID, &k.Fingerprint, &k.Message, &k.RawLine); err != nil {
			return nil, fmt.Errorf("scan ErrorEventKey: %w", err)
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// SetErrorEventFingerprint rewrites one row's fingerprint and message.
func (d *DB) SetErrorEventFingerprint(ctx context.Context, id int64, fingerprint, message string) error {
	if _, err := d.ExecContext(ctx,
		`UPDATE "ErrorEvent" SET fingerprint = $2, message = $3 WHERE id = $1`,
		id, fingerprint, message); err != nil {
		return fmt.Errorf("SetErrorEventFingerprint: %w", err)
	}
	return nil
}

// ErrorEventExists reports whether an occurrence with this pod, start
// time and fingerprint is already stored. The scanner uses it on the
// lines a previous process may have recorded before it stopped.
func (d *DB) ErrorEventExists(ctx context.Context, project, service, pod, fingerprint string, ts time.Time) (bool, error) {
	var ok bool
	err := d.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM "ErrorEvent"
		 WHERE project = $1 AND service = $2 AND ts = $3 AND pod = $4 AND fingerprint = $5)`,
		project, service, ts.UTC(), pod, fingerprint).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("ErrorEventExists: %w", err)
	}
	return ok, nil
}
