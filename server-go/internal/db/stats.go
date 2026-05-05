// Package db — runtime stats. SQLite's BUSY/wait counters disappeared
// in v0.9 with the Postgres migration; the driver's own connection
// pool + Postgres's wait_event view are the authoritative sources
// when you want operational insight. We retain a small write-error
// counter for the alert engine + admin /api/stats response.

package db

import (
	"sync/atomic"
)

// (Stats type lives in db.go alongside *DB so they're constructed
// together; this file holds the snapshot + helpers.)

// StatsSnapshot is the value-typed view returned by DB.GetStats. Atomic
// fields are not directly readable across struct copies, so we copy
// out into this concrete shape for JSON.
type StatsSnapshot struct {
	WriteErrors uint64 `json:"writeErrors"`
	// PoolOpen / PoolInUse / PoolIdle come from sql.DB.Stats() —
	// surfaced here so the admin endpoint has a single struct.
	PoolOpen   int `json:"poolOpen"`
	PoolInUse  int `json:"poolInUse"`
	PoolIdle   int `json:"poolIdle"`
}

// GetStats returns a point-in-time snapshot. Safe to call concurrently
// with writes.
func (d *DB) GetStats() StatsSnapshot {
	we := d.Stats.WriteErrors.Load()
	ps := d.DB.Stats()
	return StatsSnapshot{
		WriteErrors: we,
		PoolOpen:    ps.OpenConnections,
		PoolInUse:   ps.InUse,
		PoolIdle:    ps.Idle,
	}
}

// noOp keeps the imports honest while we stage the rest of the
// migration. Drop when stats are wired into the alert engine.
var _ = atomic.Uint64{}
