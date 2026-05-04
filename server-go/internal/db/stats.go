package db

import (
	"context"
	"database/sql"
	"strings"
	"sync/atomic"
	"time"
)

// Stats counts SQLITE_BUSY occurrences and the cumulative time the
// server spent waiting on the write lock. SQLite is configured with
// busy_timeout=5s + max-open-conns=1 (see Open), so a BUSY error
// surfaces only AFTER the driver's internal retry loop has already
// burned the full 5 seconds. That makes BusyCount a high-signal alarm:
// each tick is a request that exceeded its budget, not a contention
// blip.
//
// Counters are unbounded monotonic. Subtract two snapshots to get the
// rate over a window. Read via DB.Stats().
type Stats struct {
	// Total writes the wrapper observed.
	WriteCount atomic.Uint64
	// Subset of WriteCount that returned SQLITE_BUSY (lock not
	// acquired within busy_timeout).
	BusyCount atomic.Uint64
	// Total time spent inside ExecContext, regardless of outcome. Used
	// alongside WriteCount to compute average write latency. Stored as
	// nanoseconds so the field reads atomically.
	WriteWaitNanos atomic.Uint64
	// Time spent on the BUSY subset only. Compare against
	// WriteWaitNanos to see whether tail latency is mostly contention.
	BusyWaitNanos atomic.Uint64
}

// StatsSnapshot is the value-typed view returned by DB.Stats. Atomics
// are not directly readable across struct copies; this is the JSON-
// serializable shape the admin endpoint emits.
type StatsSnapshot struct {
	WriteCount     uint64 `json:"writeCount"`
	BusyCount      uint64 `json:"busyCount"`
	WriteWaitMs    int64  `json:"writeWaitMs"`
	BusyWaitMs     int64  `json:"busyWaitMs"`
	AvgWriteWaitMs int64  `json:"avgWriteWaitMs"`
}

// Stats returns a point-in-time snapshot of the busy/wait counters.
// Safe to call concurrently with writes.
func (d *DB) Stats() StatsSnapshot {
	wc := d.stats.WriteCount.Load()
	bc := d.stats.BusyCount.Load()
	ww := d.stats.WriteWaitNanos.Load()
	bw := d.stats.BusyWaitNanos.Load()
	out := StatsSnapshot{
		WriteCount:  wc,
		BusyCount:   bc,
		WriteWaitMs: int64(ww / uint64(time.Millisecond)),
		BusyWaitMs:  int64(bw / uint64(time.Millisecond)),
	}
	if wc > 0 {
		out.AvgWriteWaitMs = int64(ww / wc / uint64(time.Millisecond))
	}
	return out
}

// ExecContext shadows *sql.DB.ExecContext to count writes + detect
// SQLITE_BUSY. Behaviour is identical for callers; instrumentation is
// invisible.
func (d *DB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	start := time.Now()
	res, err := d.DB.ExecContext(ctx, query, args...)
	elapsed := time.Since(start)
	d.stats.WriteCount.Add(1)
	d.stats.WriteWaitNanos.Add(uint64(elapsed))
	if isBusy(err) {
		d.stats.BusyCount.Add(1)
		d.stats.BusyWaitNanos.Add(uint64(elapsed))
	}
	return res, err
}

// isBusy detects SQLITE_BUSY without importing the driver package. The
// modernc.org/sqlite driver formats the error message as "...
// (SQLITE_BUSY)" — match that and the bare "database is locked"
// phrasing some wrapped paths use.
func isBusy(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "SQLITE_BUSY") || strings.Contains(s, "database is locked")
}
