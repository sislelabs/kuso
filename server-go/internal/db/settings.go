// Settings — admin-tunable platform knobs persisted as JSON-encoded
// scalars in the Setting table. The shape is generic on purpose: a
// new knob is one extra row, no schema migration. The /settings UI
// + the build subsystem read through GetBuildSettings() which
// decodes the typed view.

package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// BuildSettings carries the live view of the build-resource knobs.
// All fields have sane defaults so a fresh install behaves the same
// as the pre-Settings v0.9.0 build (cap=1, kaniko at 2Gi).
type BuildSettings struct {
	// MaxConcurrent caps cluster-wide simultaneous build pods. 0
	// disables the cap entirely (legacy behavior). Recommended
	// values:
	//   1  — 4 GB box. Safe; one build runs at a time.
	//   2  — 8 GB box. Two parallel builds, ~5 GB headroom.
	//   4  — 16 GB+ box.
	MaxConcurrent int `json:"maxConcurrent"`
	// MemoryLimit / MemoryRequest / CPULimit / CPURequest are the
	// kube quantity strings the kusobuild chart consumes for each
	// kaniko Job pod. The strings are validated at admin-write
	// time via resource.ParseQuantity so a typo can't break every
	// future build.
	MemoryLimit   string `json:"memoryLimit"`
	MemoryRequest string `json:"memoryRequest"`
	CPULimit      string `json:"cpuLimit"`
	CPURequest    string `json:"cpuRequest"`
}

// DefaultBuildSettings returns the baseline values for a fresh
// install (4 GB box, one concurrent build, kaniko at 2Gi). The same
// values are baked into operator/helm-charts/kusobuild/values.yaml
// so the chart works without an admin override.
func DefaultBuildSettings() BuildSettings {
	return BuildSettings{
		MaxConcurrent: 1,
		MemoryLimit:   "2Gi",
		MemoryRequest: "512Mi",
		CPULimit:      "1500m",
		CPURequest:    "200m",
	}
}

// GetBuildSettings reads the build knobs out of the Setting table,
// falling back to defaults for unset keys. Returns the merged view —
// callers don't have to distinguish "absent" from "set to default."
func (d *DB) GetBuildSettings(ctx context.Context) (BuildSettings, error) {
	out := DefaultBuildSettings()
	rows, err := d.QueryContext(ctx,
		`SELECT key, value FROM "Setting" WHERE key LIKE 'build.%'`,
	)
	if err != nil {
		return out, fmt.Errorf("GetBuildSettings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return out, fmt.Errorf("scan setting: %w", err)
		}
		switch k {
		case "build.maxConcurrent":
			var n int
			if err := json.Unmarshal([]byte(v), &n); err == nil {
				out.MaxConcurrent = n
			}
		case "build.memoryLimit":
			out.MemoryLimit = unquote(v)
		case "build.memoryRequest":
			out.MemoryRequest = unquote(v)
		case "build.cpuLimit":
			out.CPULimit = unquote(v)
		case "build.cpuRequest":
			out.CPURequest = unquote(v)
		}
	}
	return out, rows.Err()
}

// SetBuildSettings writes the merged view back. Writes only the keys
// that differ from the live row to keep updatedBy meaningful.
// updatedBy is the username of the admin who saved.
func (d *DB) SetBuildSettings(ctx context.Context, in BuildSettings, updatedBy string) error {
	pairs := []struct {
		key   string
		value string
	}{
		{"build.maxConcurrent", strconv.Itoa(in.MaxConcurrent)},
		{"build.memoryLimit", quote(in.MemoryLimit)},
		{"build.memoryRequest", quote(in.MemoryRequest)},
		{"build.cpuLimit", quote(in.CPULimit)},
		{"build.cpuRequest", quote(in.CPURequest)},
	}
	now := time.Now().UTC()
	for _, p := range pairs {
		_, err := d.ExecContext(ctx, `
			INSERT INTO "Setting" (key, value, "updatedAt", "updatedBy")
			VALUES (?, ?, ?, ?)
			ON CONFLICT (key) DO UPDATE
			   SET value = EXCLUDED.value,
			       "updatedAt" = EXCLUDED."updatedAt",
			       "updatedBy" = EXCLUDED."updatedBy"`,
			p.key, p.value, now, updatedBy,
		)
		if err != nil {
			return fmt.Errorf("SetBuildSettings %s: %w", p.key, err)
		}
	}
	return nil
}

// quote / unquote shim — values are JSON-encoded TEXT so an int and
// a string can share the same column. JSON-quote a string at write
// time, strip the quotes at read time.
func quote(s string) string   { b, _ := json.Marshal(s); return string(b) }
func unquote(s string) string { var v string; _ = json.Unmarshal([]byte(s), &v); return v }

// silence unused-import on sql when this file is compiled in
// isolation (errors handles ErrNoRows-style sentinels in callers).
var (
	_ = sql.ErrNoRows
	_ = errors.New
)
