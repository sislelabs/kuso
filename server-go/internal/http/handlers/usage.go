// Cost rollup endpoints. Read-only: aggregates NodeMetric samples
// into per-day per-node usage + per-node totals, then attaches the
// cost rates the operator set in the Kuso CR's spec.cost.* keys.
//
// All authed users can hit /api/usage — there's nothing project-
// scoped to leak, just node-level usage + the operator-set rates.
// The page itself (web /settings/usage) is admin-only at the UI
// level; the underlying API is read-only and informative.

package handlers

import (
	"net/http"
	"strconv"

	"log/slog"

	"kuso/server/internal/config"
	"kuso/server/internal/db"
)

// UsageHandler owns /api/usage. Holds the DB (for the rollup query)
// and the config service (for the cost rate lookup). Both are
// required; the Mount-time nil check refuses to register the routes
// when either is missing so we don't return half-baked data.
type UsageHandler struct {
	DB     *db.DB
	Cfg    *config.Service
	Logger *slog.Logger
}

// Mount registers routes onto an authed router. Refuses to wire up
// when the dependencies aren't there — keeps the surface honest.
func (h *UsageHandler) Mount(rt interface {
	Get(string, http.HandlerFunc)
}) {
	if h.DB == nil || h.Cfg == nil {
		return
	}
	rt.Get("/api/usage", h.Get)
	rt.Get("/api/usage/projects", h.GetProjects)
}

// UsageResponse is the wire shape the /settings/usage page consumes.
// Daily is the per-(node, day) curve; Totals collapses across the
// window for the headline table. Rates are surfaced separately so
// the UI can recompute on the fly (a "what if cpu were $0.05" knob)
// without re-hitting the server.
type UsageResponse struct {
	Days   int                `json:"days"`
	Daily  []db.CostRollupDay `json:"daily"`
	Totals []db.CostTotal     `json:"totals"`
	Rates  UsageRates         `json:"rates"`
	// Projected is the next-30-days projection in operator currency.
	// Trivial extrapolation: scale Totals × (30/days). Operators see
	// "if usage continues, you'll spend $X next month" as the
	// headline.
	Projected UsageProjection `json:"projected"`
}

// UsageRates carries the configured cost rates so the UI doesn't
// have to also hit /api/config just to render a dollar number.
// Defaults are zero — operators who haven't configured rates see
// usage curves but no cost figure.
type UsageRates struct {
	CPUPerHour   float64 `json:"cpuPerHour"`
	MemGBPerHour float64 `json:"memGBPerHour"`
	Currency     string  `json:"currency"`
}

// UsageProjection collapses Daily into one "30-day" number so the UI
// can render "estimated $N this month" without a client-side sum.
type UsageProjection struct {
	CPUMilliHours int64   `json:"cpuMilliHours"`
	MemGBHours    float64 `json:"memGBHours"`
	CostTotal     float64 `json:"costTotal"`
}

// Get handles GET /api/usage?days=N (default 30, max 365).
func (h *UsageHandler) Get(w http.ResponseWriter, r *http.Request) {
	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 365 {
			days = n
		}
	}
	ctx := r.Context()
	// Log the DB error server-side; the client gets a generic message —
	// raw Postgres errors leak schema/operational detail to any authed user.
	daily, err := h.DB.CostRollup(ctx, days)
	if err != nil {
		h.logErr("usage: cost rollup", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	totals, err := h.DB.CostTotals(ctx, days)
	if err != nil {
		h.logErr("usage: cost totals", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	rates := readRates(h.Cfg)
	resp := UsageResponse{
		Days:   days,
		Daily:  daily,
		Totals: totals,
		Rates:  rates,
	}
	resp.Projected = project(totals, rates, days)
	writeJSON(w, http.StatusOK, resp)
}

// ProjectUsageRow is one project's totals + cost + share-of-cluster.
// Share is computed against the cluster total (sum across every
// project) so the UI can render a "% of cluster" column without
// re-summing client-side.
type ProjectUsageRow struct {
	Project       string  `json:"project"`
	CPUMilliHours int64   `json:"cpuMilliHours"`
	MemGBHours    float64 `json:"memGBHours"`
	Cost          float64 `json:"cost"`
	SharePct      float64 `json:"sharePct"`
}

// ProjectUsageResponse is the wire shape /settings/usage consumes.
// Daily exposes the per-(project, day) curve for trend rendering;
// Projects is the headline table. ClusterTotal is the sum across
// projects — useful as a sanity check against the per-node total.
type ProjectUsageResponse struct {
	Days         int                 `json:"days"`
	Daily        []db.ProjectCostDay `json:"daily"`
	Projects     []ProjectUsageRow   `json:"projects"`
	ClusterTotal UsageProjection     `json:"clusterTotal"`
	Rates        UsageRates          `json:"rates"`
}

// GetProjects handles GET /api/usage/projects?days=N (default 30).
// Drives the per-project rollup on the rewritten /settings/usage page.
func (h *UsageHandler) GetProjects(w http.ResponseWriter, r *http.Request) {
	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 365 {
			days = n
		}
	}
	ctx := r.Context()
	// Same generic-error policy as Get — log details, don't echo them.
	daily, err := h.DB.ProjectCostRollup(ctx, days)
	if err != nil {
		h.logErr("usage: project rollup", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	totals, err := h.DB.ProjectCostTotals(ctx, days)
	if err != nil {
		h.logErr("usage: project totals", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	rates := readRates(h.Cfg)

	scale := 30.0 / float64(days)
	if days <= 0 {
		scale = 1
	}
	var clusterCPUMilli int64
	var clusterMemGB float64
	for _, t := range totals {
		clusterCPUMilli += t.CPUMilliHours
		clusterMemGB += t.MemGBHours
	}
	projectedCPU := int64(float64(clusterCPUMilli) * scale)
	projectedMem := clusterMemGB * scale
	clusterCost := (float64(projectedCPU)/1000.0)*rates.CPUPerHour + projectedMem*rates.MemGBPerHour

	rows := make([]ProjectUsageRow, 0, len(totals))
	for _, t := range totals {
		projCPU := int64(float64(t.CPUMilliHours) * scale)
		projMem := t.MemGBHours * scale
		cost := (float64(projCPU)/1000.0)*rates.CPUPerHour + projMem*rates.MemGBPerHour
		share := 0.0
		if clusterCPUMilli > 0 {
			share = float64(t.CPUMilliHours) / float64(clusterCPUMilli) * 100
		}
		rows = append(rows, ProjectUsageRow{
			Project:       t.Project,
			CPUMilliHours: projCPU,
			MemGBHours:    projMem,
			Cost:          cost,
			SharePct:      share,
		})
	}

	writeJSON(w, http.StatusOK, ProjectUsageResponse{
		Days:     days,
		Daily:    daily,
		Projects: rows,
		ClusterTotal: UsageProjection{
			CPUMilliHours: projectedCPU,
			MemGBHours:    projectedMem,
			CostTotal:     clusterCost,
		},
		Rates: rates,
	})
}

// logErr logs an internal failure. Nil-safe on Logger so a bare
// UsageHandler in tests doesn't panic.
func (h *UsageHandler) logErr(msg string, err error) {
	if h.Logger != nil {
		h.Logger.Error(msg, "err", err)
	}
}

// readRates pulls spec.cost.{cpuPerHour, memGBPerHour, currency} off
// the Kuso CR's cached spec. Missing keys default to zero so the UI
// renders usage curves with no dollar number — that's a clearer
// signal than fabricating "$0.00 estimated" out of unset config.
func readRates(cfg *config.Service) UsageRates {
	r := UsageRates{Currency: "USD"}
	if cfg == nil {
		return r
	}
	settings := cfg.Settings()
	cost, _ := settings["cost"].(map[string]any)
	if cost == nil {
		return r
	}
	if v, ok := cost["cpuPerHour"].(float64); ok {
		r.CPUPerHour = v
	}
	if v, ok := cost["memGBPerHour"].(float64); ok {
		r.MemGBPerHour = v
	}
	if v, ok := cost["currency"].(string); ok && v != "" {
		r.Currency = v
	}
	return r
}

// project extrapolates the windowed totals to a 30-day projection
// at the configured rates. cpuMilliHours / 1000 = cpu-hours;
// memGBHours stays as-is. Sum per-node first to keep cluster total
// accurate.
func project(totals []db.CostTotal, rates UsageRates, windowDays int) UsageProjection {
	if windowDays <= 0 {
		windowDays = 30
	}
	scale := 30.0 / float64(windowDays)
	var cpuMilliHr int64
	var memGBHr float64
	for _, t := range totals {
		cpuMilliHr += t.CPUMilliHours
		memGBHr += t.MemGBHours
	}
	projectedCPU := int64(float64(cpuMilliHr) * scale)
	projectedMem := memGBHr * scale
	cost := (float64(projectedCPU) / 1000.0) * rates.CPUPerHour
	cost += projectedMem * rates.MemGBPerHour
	return UsageProjection{
		CPUMilliHours: projectedCPU,
		MemGBHours:    projectedMem,
		CostTotal:     cost,
	}
}
