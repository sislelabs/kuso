// Incident-agent configuration knobs, persisted to the Setting kv table
// under the "incident.*" key prefix. The incidents.Manager reads these via
// a cached ConfigProvider so a UI toggle hot-reloads without a redeploy.
// Mirrors the build-settings storage shape (settings.go).
package db

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// IncidentAgentConfig is the operator-tunable config for the autonomous
// incident-response agent. Stored as individual incident.* Setting rows;
// the merged view is what callers see (absent keys fall back to defaults).
type IncidentAgentConfig struct {
	Enabled       bool   `json:"enabled"`
	TriggerPod    bool   `json:"triggerPod"`
	TriggerAlert  bool   `json:"triggerAlert"`
	TriggerNode   bool   `json:"triggerNode"`
	MaxConcurrent int    `json:"maxConcurrent"`
	CooldownHours int    `json:"cooldownHours"`
	AgentImage    string `json:"agentImage,omitempty"`
}

// DefaultIncidentAgentConfig is the baseline: OFF, but with all triggers on
// and sane caps, so flipping Enabled is the only step to go live.
func DefaultIncidentAgentConfig() IncidentAgentConfig {
	return IncidentAgentConfig{
		Enabled:       false,
		TriggerPod:    true,
		TriggerAlert:  true,
		TriggerNode:   true,
		MaxConcurrent: 3,
		CooldownHours: 1,
	}
}

// GetIncidentAgentConfig returns the merged config (defaults + stored
// overrides). Never distinguishes "absent" from "set to default".
func (d *DB) GetIncidentAgentConfig(ctx context.Context) (IncidentAgentConfig, error) {
	out := DefaultIncidentAgentConfig()
	rows, err := d.QueryContext(ctx, `SELECT key, value FROM "Setting" WHERE key LIKE 'incident.%'`)
	if err != nil {
		return out, fmt.Errorf("GetIncidentAgentConfig: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return out, fmt.Errorf("scan setting: %w", err)
		}
		switch k {
		case "incident.enabled":
			out.Enabled = unquoteBool(v)
		case "incident.triggerPod":
			out.TriggerPod = unquoteBool(v)
		case "incident.triggerAlert":
			out.TriggerAlert = unquoteBool(v)
		case "incident.triggerNode":
			out.TriggerNode = unquoteBool(v)
		case "incident.maxConcurrent":
			if n, err := strconv.Atoi(v); err == nil {
				out.MaxConcurrent = n
			}
		case "incident.cooldownHours":
			if n, err := strconv.Atoi(v); err == nil {
				out.CooldownHours = n
			}
		case "incident.agentImage":
			out.AgentImage = unquote(v)
		}
	}
	return out, rows.Err()
}

// SetIncidentAgentConfig upserts all incident.* keys from the merged view.
func (d *DB) SetIncidentAgentConfig(ctx context.Context, in IncidentAgentConfig, updatedBy string) error {
	pairs := []struct{ key, value string }{
		{"incident.enabled", boolStr(in.Enabled)},
		{"incident.triggerPod", boolStr(in.TriggerPod)},
		{"incident.triggerAlert", boolStr(in.TriggerAlert)},
		{"incident.triggerNode", boolStr(in.TriggerNode)},
		{"incident.maxConcurrent", strconv.Itoa(in.MaxConcurrent)},
		{"incident.cooldownHours", strconv.Itoa(in.CooldownHours)},
		{"incident.agentImage", quote(in.AgentImage)},
	}
	now := time.Now().UTC()
	for _, p := range pairs {
		if _, err := d.ExecContext(ctx, `
			INSERT INTO "Setting" (key, value, "updatedAt", "updatedBy")
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (key) DO UPDATE
			   SET value = EXCLUDED.value,
			       "updatedAt" = EXCLUDED."updatedAt",
			       "updatedBy" = EXCLUDED."updatedBy"`,
			p.key, p.value, now, updatedBy); err != nil {
			return fmt.Errorf("SetIncidentAgentConfig %s: %w", p.key, err)
		}
	}
	return nil
}

// IncidentAgentConfigExists reports whether any incident.* key is stored
// (used to seed-once from env on first boot).
func (d *DB) IncidentAgentConfigExists(ctx context.Context) (bool, error) {
	var n int
	err := d.QueryRowContext(ctx, `SELECT count(*) FROM "Setting" WHERE key LIKE 'incident.%'`).Scan(&n)
	return n > 0, err
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func unquoteBool(s string) bool {
	var v bool
	_ = json.Unmarshal([]byte(s), &v)
	return v
}
