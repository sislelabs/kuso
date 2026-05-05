// Package errorscan walks LogLine rows looking for error patterns
// and writes one ErrorEvent row per match. Runs as a singleton
// goroutine in kuso-server (R4 compliant — uses workerCtx so a
// SIGTERM gives it time to flush).
//
// Why not "just grep the logs at query time": pattern-matching 14
// days of LogLine on every dashboard render burns CPU + I/O the
// alert engine and the log-search handler also need. Pre-aggregating
// at write time costs ~1ms per matched line and the dashboard reads
// from an indexed table.
//
// Fingerprint scheme: take the first ~200 chars of the matched
// message, lowercase, strip numbers, hex, UUIDs, IP-like dotted
// segments, and quoted strings. SHA256 the result and keep 16 hex
// chars. Two hits with different request IDs but the same code path
// collapse to the same fingerprint.

package errorscan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"kuso/server/internal/db"
)

// patterns is the list of regexes that flag a log line as an error.
// Order matters — the first match wins. Keeping the list small +
// case-insensitive so we don't burn CPU on every line.
var errorPatterns = []*regexp.Regexp{
	// Generic ERROR / FATAL prefixes (logging frameworks).
	regexp.MustCompile(`(?i)\b(?:ERROR|FATAL|SEVERE|PANIC)\b`),
	// Go panic.
	regexp.MustCompile(`^panic:\s`),
	// Python traceback / exception.
	regexp.MustCompile(`(?i)Traceback \(most recent call last\)`),
	regexp.MustCompile(`(?i)\bException\b.*?:`),
	// Node.js unhandled.
	regexp.MustCompile(`(?i)UnhandledPromiseRejection|UnhandledRejection`),
	// Java / JVM stacktraces.
	regexp.MustCompile(`(?i)\bException in thread\b`),
	regexp.MustCompile(`(?i)\bjava\.lang\.\w+Exception\b`),
	// Ruby on Rails.
	regexp.MustCompile(`(?i)\bActionController::|\bActiveRecord::`),
	// HTTP 5xx in structured logs.
	regexp.MustCompile(`(?i)"status"\s*:\s*5\d\d\b`),
}

// Scanner scans LogLine for error rows and writes ErrorEvent.
type Scanner struct {
	DB       *db.DB
	Logger   *slog.Logger
	Interval time.Duration
	// BatchSize caps how many LogLine rows we pull per tick. Larger
	// batches close the catch-up gap faster on a backlog but burn
	// more memory; 500 is a comfortable middle ground for a 4 GB box.
	BatchSize int
}

const watermarkKey = "errorscan.lastLogLineId"

// Run is the goroutine entrypoint. Returns when ctx is canceled.
func (s *Scanner) Run(ctx context.Context) {
	if s.Interval <= 0 {
		s.Interval = 30 * time.Second
	}
	if s.BatchSize <= 0 {
		s.BatchSize = 500
	}
	if s.Logger == nil {
		s.Logger = slog.Default()
	}
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	// Run a tick immediately so a fresh boot doesn't wait Interval
	// before processing the first batch.
	s.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.tick(ctx)
		}
	}
}

// tick: read watermark → fetch next batch of LogLine → scan → insert
// matches → save new watermark. Best-effort throughout: a DB error
// during a batch logs and stops *this* tick; the next tick retries
// from the same watermark.
func (s *Scanner) tick(ctx context.Context) {
	if s.DB == nil {
		return
	}
	wm, err := s.DB.ScannerWatermark(ctx, watermarkKey)
	if err != nil {
		s.Logger.Warn("errorscan: read watermark", "err", err)
		return
	}
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, ts, pod, project, service, env, line
		FROM "LogLine"
		WHERE id > ?
		ORDER BY id ASC
		LIMIT ?`,
		wm, s.BatchSize,
	)
	if err != nil {
		s.Logger.Warn("errorscan: query log lines", "err", err)
		return
	}
	defer rows.Close()

	matched := 0
	scanned := 0
	maxID := wm
	for rows.Next() {
		var id int64
		var ts time.Time
		var pod, project, service, env, line string
		if err := rows.Scan(&id, &ts, &pod, &project, &service, &env, &line); err != nil {
			s.Logger.Warn("errorscan: scan row", "err", err)
			continue
		}
		scanned++
		if id > maxID {
			maxID = id
		}
		if !matchesAnyPattern(line) {
			continue
		}
		fp := fingerprintFor(line)
		msg := truncateForMessage(line)
		if err := s.DB.InsertErrorEvent(ctx, db.ErrorEvent{
			Project:     project,
			Service:     service,
			Env:         env,
			Pod:         pod,
			Fingerprint: fp,
			Message:     msg,
			RawLine:     truncateForRaw(line),
			Ts:          ts,
		}); err != nil {
			s.Logger.Warn("errorscan: insert", "err", err)
			continue
		}
		matched++
	}
	if err := rows.Err(); err != nil {
		s.Logger.Warn("errorscan: rows iter", "err", err)
	}
	if maxID > wm {
		if err := s.DB.SaveScannerWatermark(ctx, watermarkKey, maxID); err != nil {
			s.Logger.Warn("errorscan: save watermark", "err", err)
		}
	}
	if scanned > 0 {
		s.Logger.Debug("errorscan: tick", "scanned", scanned, "matched", matched, "watermark", maxID)
	}
}

// matchesAnyPattern returns true if any of the error regexes hits.
// Cheap iteration — these patterns are deliberately small.
func matchesAnyPattern(line string) bool {
	for _, p := range errorPatterns {
		if p.MatchString(line) {
			return true
		}
	}
	return false
}

// normalize: lowercase, strip numbers, hex blobs, UUIDs, IP-like
// segments, and quoted strings. Used for fingerprinting so noisy
// variants (request IDs, timestamps embedded in messages) collapse
// to the same hash.
var (
	hexRe   = regexp.MustCompile(`\b[0-9a-fA-F]{8,}\b`)
	uuidRe  = regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`)
	numRe   = regexp.MustCompile(`\b\d+\b`)
	quoteRe = regexp.MustCompile(`"[^"]*"|'[^']*'`)
)

// fingerprintFor returns a 16-char hex hash of the normalized line.
// Truncates the line to 256 chars before normalizing — a 5KB stack
// trace would otherwise produce a different fingerprint for every
// occurrence.
func fingerprintFor(line string) string {
	if len(line) > 256 {
		line = line[:256]
	}
	n := strings.ToLower(line)
	n = uuidRe.ReplaceAllString(n, "<uuid>")
	n = hexRe.ReplaceAllString(n, "<hex>")
	n = numRe.ReplaceAllString(n, "<n>")
	n = quoteRe.ReplaceAllString(n, "<str>")
	sum := sha256.Sum256([]byte(n))
	return hex.EncodeToString(sum[:8])
}

// truncateForMessage caps the stored "human-readable summary" at
// ~250 chars. The full line lives in rawLine.
func truncateForMessage(line string) string {
	line = strings.TrimSpace(line)
	const max = 250
	if len(line) > max {
		return line[:max] + "…"
	}
	return line
}

// truncateForRaw is generous for the sample line shown in the UI
// drill-down; cap at 4KB so a malicious log payload can't blow up
// a row.
func truncateForRaw(line string) string {
	const max = 4096
	if len(line) > max {
		return line[:max] + "…"
	}
	return line
}
