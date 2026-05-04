// Searchable log storage types. The actual storage methods live on
// *LogDB in log_db.go after the v0.7.17 split — see the package
// comment there for the rationale. This file keeps only the shared
// row + request structs so callers across the codebase don't have to
// chase a moved import.

package db

import "time"

type LogLine struct {
	ID      int64     `json:"id"`
	Ts      time.Time `json:"ts"`
	Pod     string    `json:"pod"`
	Project string    `json:"project,omitempty"`
	Service string    `json:"service,omitempty"`
	Env     string    `json:"env,omitempty"`
	Line    string    `json:"line"`
}

// SearchLogsRequest is the wire shape — every field optional except
// project + service (which gate access at the handler layer).
type SearchLogsRequest struct {
	Project string
	Service string
	Env     string
	Query   string    // FTS5 MATCH; empty means "no text filter"
	Since   time.Time // inclusive; zero = no lower bound
	Until   time.Time // exclusive; zero = no upper bound
	Limit   int
}
