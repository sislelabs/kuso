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
	"unicode/utf8"

	"kuso/server/internal/db"
	"kuso/server/internal/serverstate"
)

// patterns is the list of regexes that flag a log line as an error.
// Order matters — the first match wins. Keeping the list small +
// case-insensitive so we don't burn CPU on every line.
var errorPatterns = []*regexp.Regexp{
	// Generic ERROR / FATAL keywords (logging frameworks). Delimited by
	// whitespace or punctuation rather than \b: a "." or "/" also counts
	// as a word boundary, so the bot-probe path /manager.php/error.php
	// matched. Must stay first — matchesAnyPattern skips it on lines
	// with an explicit info/debug level.
	regexp.MustCompile(`(?i)(?:^|[\s\[\("'=:|,{])(?:ERROR|FATAL|SEVERE|PANIC)(?:$|[\s\]\)"':,|}])`),
	// nginx / syslog fatal levels, bracketed ("2026/11/09 12:00:00 [emerg]
	// 1#1: bind() failed"). Bracketed only: the bare words are ordinary
	// prose ("alert", "critical path").
	regexp.MustCompile(`(?i)\[(?:emerg|alert|crit|critical)\]`),
	// Go panic.
	regexp.MustCompile(`^panic:\s`),
	// Python traceback / exception.
	regexp.MustCompile(`(?i)Traceback \(most recent call last\)`),
	regexp.MustCompile(`(?i)\bException\b.*?:`),
	// Error class names (TypeError, AggregateError, ValueError,
	// NullPointerException). Case-sensitive so "multierror" and
	// "ErrorBoundary" stay out.
	regexp.MustCompile(`\b[A-Z][A-Za-z]*(?:Error|Exception)\b`),
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
	// MaxBatchesPerTick and TickBudget bound one tick's catch-up loop,
	// which runs on the leader: it stops at whichever comes first, or
	// at a short batch (caught up).
	MaxBatchesPerTick int
	TickBudget        time.Duration
	// MaxLag is how many LogLine rows the watermark may trail max(id).
	// Further behind, the scanner jumps to max(id)-MaxLag: those lines
	// are near the prune edge and nobody reads week-old error groups.
	MaxLag int64
	// Pods is logship's record of which pods are builds/release hooks.
	// Optional: nil, or a pod it hasn't seen, falls back to a heuristic.
	Pods *PodKinds
}

const watermarkKey = "errorscan.lastLogLineId"

// HeartbeatInterval is the scanner's default tick cadence, exported so
// main.go can register errorscan in the serverstate liveness registry at
// the interval it beats. main.go sets Scanner.Interval to this same value,
// and Run falls back to it when Interval is unset.
const HeartbeatInterval = 30 * time.Second

// Run is the goroutine entrypoint. Returns when ctx is canceled.
func (s *Scanner) Run(ctx context.Context) {
	if s.Interval <= 0 {
		s.Interval = HeartbeatInterval
	}
	s.applyDefaults()
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
			// Liveness heartbeat: the loop completed an iteration (even one
			// that hit a DB error and logged — the goroutine is alive). A
			// no-op unless registered (leader-gated in startSingletons).
			serverstate.LoopHeartbeat(serverstate.LoopErrorScan)
		}
	}
}

func (s *Scanner) applyDefaults() {
	if s.BatchSize <= 0 {
		s.BatchSize = 500
	}
	if s.MaxBatchesPerTick <= 0 {
		s.MaxBatchesPerTick = 40
	}
	if s.TickBudget <= 0 {
		s.TickBudget = 20 * time.Second
	}
	if s.MaxLag <= 0 {
		s.MaxLag = 100_000
	}
	if s.Logger == nil {
		s.Logger = slog.Default()
	}
}

// tick: read watermark → skip ahead if hopelessly behind → scan batches
// until caught up or the per-tick bound is hit. Best-effort throughout:
// a DB error logs and stops *this* tick; the next tick retries from the
// last saved watermark.
func (s *Scanner) tick(ctx context.Context) {
	if s.DB == nil {
		return
	}
	s.applyDefaults()
	wm, err := s.DB.ScannerWatermark(ctx, watermarkKey)
	if err != nil {
		s.Logger.Warn("errorscan: read watermark", "err", err)
		return
	}
	if err := s.refingerprintStep(ctx); err != nil {
		s.Logger.Warn("errorscan: rewrite fingerprints", "err", err)
	}
	wm, err = s.skipAhead(ctx, wm)
	if err != nil {
		s.Logger.Warn("errorscan: skip ahead", "err", err)
		return
	}
	deadline := time.Now().Add(s.TickBudget)
	for i := 0; i < s.MaxBatchesPerTick && ctx.Err() == nil; i++ {
		scanned, next, err := s.scanBatch(ctx, wm)
		if err != nil {
			s.Logger.Warn("errorscan: batch", "err", err)
			return
		}
		wm = next
		if scanned < s.BatchSize || time.Now().After(deadline) {
			return
		}
	}
}

// skipAhead moves the watermark to max(id)-MaxLag when it trails further
// than that, and returns the watermark to scan from.
func (s *Scanner) skipAhead(ctx context.Context, wm int64) (int64, error) {
	var maxID int64
	if err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM "LogLine"`).Scan(&maxID); err != nil {
		return wm, err
	}
	if maxID-wm <= s.MaxLag {
		return wm, nil
	}
	target := maxID - s.MaxLag
	if err := s.DB.SaveScannerWatermark(ctx, watermarkKey, target); err != nil {
		return wm, err
	}
	s.Logger.Warn("errorscan: backlog too large, skipping ahead",
		"from", wm, "to", target, "skippedRows", target-wm)
	return target, nil
}

// scanBatch scans up to BatchSize LogLine rows after wm, records matches
// and saves the new watermark. Returns rows scanned and the new watermark.
func (s *Scanner) scanBatch(ctx context.Context, wm int64) (int, int64, error) {
	// $N placeholders, NOT `?`. The driver is lib/pq (see internal/db),
	// which passes `?` through as a literal — the query then fails to
	// parse on EVERY tick, so this scanner silently never populated
	// ErrorEvent. Same class of bug as the one documented in
	// handlers/log_search.go; the db package's `?`→$N shim was removed
	// in v0.18 and this query was missed. Guarded by TestTickQueryUsesPositionalPlaceholders.
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, ts, pod, project, service, env, line
		FROM "LogLine"
		WHERE id > $1
		ORDER BY id ASC
		LIMIT $2`,
		wm, s.BatchSize,
	)
	if err != nil {
		return 0, wm, err
	}
	defer rows.Close()

	matched := 0
	scanned := 0
	maxID := wm
	g := newGrouper(func(c *errorChain) {
		title := canonicalTitle(c.title, c.lines)
		if err := s.DB.InsertErrorEvent(ctx, db.ErrorEvent{
			Project:     c.head.project,
			Service:     c.head.service,
			Env:         c.head.env,
			Pod:         c.head.pod,
			Fingerprint: fingerprintFor(title),
			Message:     truncateForMessage(title),
			RawLine:     truncateForRaw(c.raw()),
			Ts:          c.head.ts,
		}); err != nil {
			s.Logger.Warn("errorscan: insert", "err", err)
			return
		}
		matched++
	})
	for rows.Next() {
		var l scanLine
		if err := rows.Scan(&l.id, &l.ts, &l.pod, &l.project, &l.service, &l.env, &l.line); err != nil {
			s.Logger.Warn("errorscan: scan row", "err", err)
			continue
		}
		scanned++
		if l.id > maxID {
			maxID = l.id
		}
		if !s.runtimeLine(l.pod, l.line) {
			continue
		}
		g.feed(l, matchesAnyPattern(l.line))
	}
	// A chain cut by the batch boundary is recorded as it stands; the
	// watermark below moves past its lines either way.
	g.flush()
	if err := rows.Err(); err != nil {
		return scanned, wm, err
	}
	if maxID > wm {
		if err := s.DB.SaveScannerWatermark(ctx, watermarkKey, maxID); err != nil {
			return scanned, wm, err
		}
	}
	if scanned > 0 {
		s.Logger.Debug("errorscan: batch", "scanned", scanned, "matched", matched, "watermark", maxID)
	}
	return scanned, maxID, nil
}

// matchesAnyPattern returns true if any of the error regexes hits.
// Cheap iteration — these patterns are deliberately small.
func matchesAnyPattern(line string) bool {
	quiet := quietLevelRe.MatchString(line)
	for i, p := range errorPatterns {
		if i == 0 && quiet {
			continue
		}
		if p.MatchString(line) {
			return true
		}
	}
	return false
}

// quietLevelRe matches a structured log line that declares itself
// info/debug/trace. On those, a bare "error" word is part of the message
// or a field value (a request path, a counter name), not the severity.
var quietLevelRe = regexp.MustCompile(`(?i)(?:\blevel=|"level"\s*:\s*")(?:info|debug|trace)\b`)

// normalize: lowercase, then replace the volatile tokens (UUIDs, hex
// ids, numbers) and quoted strings with placeholders, so variants that
// differ only in request ids, timestamps, durations, IPs or ports
// collapse to one hash.
//
// Numbers are matched without word boundaries: a lowercased ISO
// timestamp ("2026-11-08t19:40:05z"), a duration ("250ms", "5m0s") or a
// pointer ("0xc0001a2") glues digits to letters, and a \b-anchored match
// left those in, giving every occurrence its own group. Hex ids and
// numbers share one placeholder so an id that happens to be all digits
// (or crosses the 8-char hex threshold) still lands in the same group.
var (
	hexRe   = regexp.MustCompile(`\b(?:0x[0-9a-f]+|[0-9a-f]{8,})\b`)
	uuidRe  = regexp.MustCompile(`\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	numRe   = regexp.MustCompile(`\d+`)
	quoteRe = regexp.MustCompile(`"[^"]*"|'[^']*'`)
)

// fingerprintFor returns a 16-char hex hash of the normalized line.
// Caps the normalized form at 256 chars — a 5KB stack
// trace would otherwise produce a different fingerprint for every
// occurrence.
func fingerprintFor(line string) string {
	// Normalize first, cut after: cutting the raw line made the cut point
	// depend on how many digits the ids and IPs had ("*109" vs "*1320"),
	// which could split a quoted string and give the same error two groups.
	if len(line) > 2048 {
		line = line[:2048]
	}
	n := strings.ToLower(line)
	n = uuidRe.ReplaceAllString(n, "<id>")
	n = hexRe.ReplaceAllString(n, "<n>")
	n = numRe.ReplaceAllString(n, "<n>")
	n = quoteRe.ReplaceAllString(n, "<str>")
	if len(n) > 256 {
		n = n[:256]
	}
	sum := sha256.Sum256([]byte(n))
	return hex.EncodeToString(sum[:8])
}

// truncateForMessage caps the stored "human-readable summary" at
// ~250 chars. The full line lives in rawLine.
func truncateForMessage(line string) string {
	line = strings.TrimSpace(line)
	const max = 250
	if len(line) > max {
		return cutAtRune(line, max) + "…"
	}
	return line
}

// cutAtRune returns s cut to at most max bytes without splitting a
// multi-byte rune: Postgres rejects invalid UTF-8 in TEXT.
func cutAtRune(s string, max int) string {
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}

// truncateForRaw is generous for the sample line shown in the UI
// drill-down; cap at 4KB so a malicious log payload can't blow up
// a row.
func truncateForRaw(line string) string {
	const max = 4096
	if len(line) > max {
		return cutAtRune(line, max) + "…"
	}
	return line
}
