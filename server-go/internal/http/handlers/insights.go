package handlers

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"kuso/server/internal/kube"
	"kuso/server/internal/podusage"
)

// insightsRange is one of the page's range presets. The step keeps every
// series at 60-150 points; usage history only goes back podusage.Retention.
type insightsRange struct {
	dur  time.Duration
	step time.Duration
}

var insightsRanges = map[string]insightsRange{
	"1h":  {time.Hour, time.Minute},
	"6h":  {6 * time.Hour, 5 * time.Minute},
	"24h": {24 * time.Hour, 10 * time.Minute},
}

// insightsResponse is GET /api/insights: per-service series for every
// production env the caller can see, plus the deploys in the range.
// Series are [unixSeconds, value]: requests per minute, CPU millicores,
// memory bytes, p95 latency ms, 5xx share of requests in percent.
type insightsResponse struct {
	Range             string    `json:"range"`
	StepSeconds       int       `json:"stepSeconds"`
	Since             time.Time `json:"since"`
	Until             time.Time `json:"until"`
	RequestsAvailable bool      `json:"requestsAvailable"`
	UsageAvailable    bool      `json:"usageAvailable"`
	// UsageSince is the oldest CPU/memory sample held. The history is in
	// memory, so it starts at the server's last restart.
	UsageSince *time.Time        `json:"usageSince,omitempty"`
	Services   []insightsService `json:"services"`
	Deploys    []insightsDeploy  `json:"deploys"`
}

type insightsService struct {
	Project   string       `json:"project"`
	Service   string       `json:"service"`
	Env       string       `json:"env"`
	Requests  [][2]float64 `json:"requests"`
	CPU       [][2]float64 `json:"cpu"`
	Memory    [][2]float64 `json:"memory"`
	P95       [][2]float64 `json:"p95"`
	ErrorRate [][2]float64 `json:"errorRate"`
}

type insightsDeploy struct {
	Project       string    `json:"project"`
	Service       string    `json:"service"`
	At            time.Time `json:"at"`
	CommitSha     string    `json:"commitSha"`
	CommitMessage string    `json:"commitMessage"`
}

// insightsTraffic holds the three Traefik range queries for each range
// preset, shared by every caller for pulseTTL.
type insightsTraffic struct {
	req, errs, p95 map[string][][2]float64
}

var insightsCache struct {
	mu sync.Mutex
	at map[string]time.Time
	v  map[string]insightsTraffic
}

func cachedInsightsTraffic(ctx context.Context, key string, rg insightsRange, now time.Time) (insightsTraffic, error) {
	insightsCache.mu.Lock()
	defer insightsCache.mu.Unlock()
	if t, ok := insightsCache.at[key]; ok && now.Sub(t) < pulseTTL {
		return insightsCache.v[key], nil
	}
	step := rg.step.String()
	win := rateWindow(step)
	start := now.Add(-rg.dur)
	var out insightsTraffic
	var err error
	if out.req, err = promQueryRangeBySeries(ctx, fmt.Sprintf(`sum by (service) (rate(traefik_service_requests_total[%s]))`, win), "service", start, now, step); err != nil {
		return out, err
	}
	if out.errs, err = promQueryRangeBySeries(ctx, fmt.Sprintf(`sum by (service) (rate(traefik_service_requests_total{code=~"5.."}[%s]))`, win), "service", start, now, step); err != nil {
		return out, err
	}
	if out.p95, err = promQueryRangeBySeries(ctx, fmt.Sprintf(`1000 * histogram_quantile(0.95, sum by (service, le) (rate(traefik_service_request_duration_seconds_bucket[%s])))`, win), "service", start, now, step); err != nil {
		return out, err
	}
	if insightsCache.at == nil {
		insightsCache.at, insightsCache.v = map[string]time.Time{}, map[string]insightsTraffic{}
	}
	insightsCache.at[key], insightsCache.v[key] = now, out
	return out, nil
}

// Insights is GET /api/insights?range=1h|6h|24h.
func (h *ProjectsHandler) Insights(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("range")
	if key == "" {
		key = "24h"
	}
	rg, ok := insightsRanges[key]
	if !ok {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("invalid range %q: use 1h, 6h or 24h", key))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	all, err := h.Svc.List(ctx)
	if err != nil {
		h.fail(w, "insights", err)
		return
	}
	visible, ok := h.filterProjectsForCaller(ctx, w, all)
	if !ok {
		return
	}
	now := time.Now().UTC()
	out := insightsResponse{
		Range:       key,
		StepSeconds: int(rg.step.Seconds()),
		Since:       now.Add(-rg.dur),
		Until:       now,
		Services:    []insightsService{},
		Deploys:     []insightsDeploy{},
	}
	projects := make(map[string]bool, len(visible))
	namespaces := map[string]struct{}{}
	for i := range visible {
		projects[visible[i].Name] = true
		ns := visible[i].Spec.Namespace
		if ns == "" {
			ns = h.Namespace
		}
		namespaces[ns] = struct{}{}
	}

	if h.Kube != nil {
		// envKey ("<ns>-<env CR>") is how Traefik names the env's backend.
		byEnvKey := map[string]int{}
		for ns := range namespaces {
			envs, lerr := h.Kube.ListKusoEnvironments(ctx, ns)
			if lerr != nil {
				h.Logger.Warn("insights: list envs", "namespace", ns, "err", lerr)
				continue
			}
			for i := range envs {
				e := &envs[i]
				if !projects[e.Spec.Project] || !isProductionEnv(e) {
					continue
				}
				byEnvKey[ns+"-"+e.Name] = len(out.Services)
				out.Services = append(out.Services, insightsService{
					Project: e.Spec.Project,
					Service: strings.TrimPrefix(e.Spec.Service, e.Spec.Project+"-"),
					Env:     e.Name,
				})
			}
		}
		if tr, terr := cachedInsightsTraffic(ctx, key, rg, now); terr != nil {
			h.Logger.Warn("insights: prometheus", "err", terr)
		} else {
			out.RequestsAvailable = true
			fillTraffic(out.Services, byEnvKey, tr)
		}
	}
	for i := range out.Services {
		s := &out.Services[i]
		s.CPU, s.Memory = [][2]float64{}, [][2]float64{}
		if h.Usage == nil {
			continue
		}
		hist := h.Usage.History(podusage.Key{Project: s.Project, Service: s.Service, Env: s.Env}, now, min(rg.dur, podusage.Retention), int(rg.dur/rg.step))
		if hist.Since != nil {
			t := time.UnixMilli(*hist.Since).UTC()
			if out.UsageSince == nil || t.Before(*out.UsageSince) {
				out.UsageSince = &t
			}
		}
		for _, p := range hist.Samples {
			t := float64(p.T / 1000)
			s.CPU = append(s.CPU, [2]float64{t, p.CPUMilli})
			s.Memory = append(s.Memory, [2]float64{t, float64(p.MemBytes)})
		}
	}
	out.UsageAvailable = h.Usage != nil
	sort.Slice(out.Services, func(i, j int) bool {
		a, b := out.Services[i], out.Services[j]
		if a.Project != b.Project {
			return a.Project < b.Project
		}
		return a.Service < b.Service
	})

	if h.DB != nil {
		recs, derr := h.DB.ListSucceededBuildRecordsSince(ctx, out.Since)
		if derr != nil {
			h.Logger.Warn("insights: build records", "err", derr)
		}
		// Only deploys of the services on the page: env-group clones
		// (web-staging) build too, but they aren't production.
		shown := make(map[string]bool, len(out.Services))
		for _, s := range out.Services {
			shown[s.Project+"/"+s.Service] = true
		}
		for _, rec := range recs {
			svc := strings.TrimPrefix(rec.Service, rec.Project+"-")
			if projects[rec.Project] && (h.Kube == nil || shown[rec.Project+"/"+svc]) {
				out.Deploys = append(out.Deploys, insightsDeploy{
					Project:       rec.Project,
					Service:       svc,
					At:            rec.CreatedAt.UTC(),
					CommitSha:     rec.CommitSha,
					CommitMessage: firstLine(rec.CommitMessage),
				})
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func isProductionEnv(e *kube.KusoEnvironment) bool {
	if v := e.Labels[kube.LabelEnv]; v != "" {
		return v == "production"
	}
	return e.Spec.Kind == "production"
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// fillTraffic maps Traefik's per-backend series onto services. An env
// can have one backend per port: requests and 5xx add up, p95 takes the
// slowest port. Requests become per-minute and 5xx a percentage.
func fillTraffic(svcs []insightsService, byEnvKey map[string]int, tr insightsTraffic) {
	sum := func(a, b float64) float64 { return a + b }
	req := mergeByEnv(tr.req, byEnvKey, sum)
	errs := mergeByEnv(tr.errs, byEnvKey, sum)
	p95 := mergeByEnv(tr.p95, byEnvKey, math.Max)
	for i := range svcs {
		s := &svcs[i]
		s.Requests, s.P95, s.ErrorRate = [][2]float64{}, [][2]float64{}, [][2]float64{}
		for _, t := range sortedTimes(req[i]) {
			rps := req[i][t]
			s.Requests = append(s.Requests, [2]float64{t, rps * 60})
			pct := 0.0
			if rps > 0 {
				pct = errs[i][t] / rps * 100
			}
			s.ErrorRate = append(s.ErrorRate, [2]float64{t, pct})
		}
		for _, t := range sortedTimes(p95[i]) {
			s.P95 = append(s.P95, [2]float64{t, p95[i][t]})
		}
	}
}

func mergeByEnv(series map[string][][2]float64, byEnvKey map[string]int, merge func(a, b float64) float64) map[int]map[float64]float64 {
	out := map[int]map[float64]float64{}
	for label, pts := range series {
		env, ok := traefikServiceEnv(label)
		if !ok {
			continue
		}
		i, ok := byEnvKey[env]
		if !ok {
			continue
		}
		m := out[i]
		if m == nil {
			m = map[float64]float64{}
			out[i] = m
		}
		for _, p := range pts {
			if old, seen := m[p[0]]; seen {
				m[p[0]] = merge(old, p[1])
			} else {
				m[p[0]] = p[1]
			}
		}
	}
	return out
}

func sortedTimes(m map[float64]float64) []float64 {
	ks := make([]float64, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Float64s(ks)
	return ks
}
