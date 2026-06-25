// usage.go — client methods for the read-only cost-rollup surface the
// web /settings/usage page drives: a cluster/per-node rollup and a
// per-project breakdown. Both are GET-only and accept an optional
// ?days=N window (default 30, max 365 server-side).
//
// Follows the projects.go idiom: take args, build the path, return the
// raw (*resty.Response, error) so the command layer maps status codes.

package kusoApi

import (
	"strconv"

	"github.com/go-resty/resty/v2"
)

// UsageRates carries the operator-configured cost rates the server
// surfaces alongside the usage curves so the CLI can render a dollar
// figure without a second /api/config call. Zero rates mean the
// operator hasn't configured costs — the CLI shows usage but no cost.
type UsageRates struct {
	CPUPerHour   float64 `json:"cpuPerHour"`
	MemGBPerHour float64 `json:"memGBPerHour"`
	Currency     string  `json:"currency"`
}

// UsageProjection is the 30-day extrapolation of the windowed totals
// at the configured rates. Used for the headline "estimated $N this
// month" figure.
type UsageProjection struct {
	CPUMilliHours int64   `json:"cpuMilliHours"`
	MemGBHours    float64 `json:"memGBHours"`
	CostTotal     float64 `json:"costTotal"`
}

// CostTotal is one node's totals over the window. Mirrors
// server-go internal/db.CostTotal.
type CostTotal struct {
	Node          string  `json:"node"`
	CPUMilliHours int64   `json:"cpuMilliHours"`
	MemGBHours    float64 `json:"memGBHours"`
	Days          int     `json:"days"`
}

// CostRollupDay is one (node, day) usage bucket. Mirrors
// server-go internal/db.CostRollupDay.
type CostRollupDay struct {
	Node          string `json:"node"`
	Day           string `json:"day"`
	CPUMilliHours int64  `json:"cpuMilliHours"`
	MemGBHours    float64 `json:"memGBHours"`
	SampleCount   int    `json:"sampleCount"`
}

// UsageResponse is the wire shape of GET /api/usage. Daily is the
// per-(node, day) curve; Totals collapses across the window for the
// headline table; Projected is the next-30-days extrapolation.
type UsageResponse struct {
	Days      int             `json:"days"`
	Daily     []CostRollupDay `json:"daily"`
	Totals    []CostTotal     `json:"totals"`
	Rates     UsageRates      `json:"rates"`
	Projected UsageProjection `json:"projected"`
}

// ProjectUsageRow is one project's totals + cost + share-of-cluster.
// Mirrors server-go handlers.ProjectUsageRow.
type ProjectUsageRow struct {
	Project       string  `json:"project"`
	CPUMilliHours int64   `json:"cpuMilliHours"`
	MemGBHours    float64 `json:"memGBHours"`
	Cost          float64 `json:"cost"`
	SharePct      float64 `json:"sharePct"`
}

// ProjectCostDay is one (project, day) usage bucket. Mirrors
// server-go internal/db.ProjectCostDay.
type ProjectCostDay struct {
	Project       string  `json:"project"`
	Day           string  `json:"day"`
	CPUMilliHours int64   `json:"cpuMilliHours"`
	MemGBHours    float64 `json:"memGBHours"`
	SampleCount   int     `json:"sampleCount"`
}

// ProjectUsageResponse is the wire shape of GET /api/usage/projects.
type ProjectUsageResponse struct {
	Days         int               `json:"days"`
	Daily        []ProjectCostDay  `json:"daily"`
	Projects     []ProjectUsageRow `json:"projects"`
	ClusterTotal UsageProjection   `json:"clusterTotal"`
	Rates        UsageRates        `json:"rates"`
}

// Usage returns the cluster/per-node cost rollup over the last `days`
// days (0 = server default of 30). Response: UsageResponse.
func (k *KusoClient) Usage(days int) (*resty.Response, error) {
	return k.client.Get("/api/usage" + daysQuery(days))
}

// ProjectUsage returns the per-project cost breakdown over the last
// `days` days (0 = server default of 30). Response: ProjectUsageResponse.
func (k *KusoClient) ProjectUsage(days int) (*resty.Response, error) {
	return k.client.Get("/api/usage/projects" + daysQuery(days))
}

// daysQuery renders the optional ?days=N suffix; empty when days <= 0
// so the server applies its own default.
func daysQuery(days int) string {
	if days <= 0 {
		return ""
	}
	return "?days=" + strconv.Itoa(days)
}
