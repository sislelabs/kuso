package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	pulseRange = 24 * time.Hour
	pulseStep  = "10m"
	pulseTTL   = time.Minute
)

// projectsPulseResponse is GET /api/projects/pulse: the last 24h of
// traffic and deploys across every project the caller can see, for the
// home page's fleet strip.
type projectsPulseResponse struct {
	Since       time.Time `json:"since"`
	StepSeconds int       `json:"stepSeconds"`
	// Requests is [unixSeconds, requests per minute], summed across the
	// visible projects' environments. Empty when Prometheus is down.
	Requests          [][2]float64  `json:"requests"`
	RequestsAvailable bool          `json:"requestsAvailable"`
	Deploys           []pulseDeploy `json:"deploys"`
}

type pulseDeploy struct {
	Project string    `json:"project"`
	Service string    `json:"service"`
	At      time.Time `json:"at"`
}

// trafficBySeries is one query_range result keyed by Traefik's service
// label, shared by every caller for pulseTTL so the home page polling
// doesn't put one Prometheus range query per viewer per minute.
var pulseTraffic struct {
	mu     sync.Mutex
	at     time.Time
	series map[string][][2]float64
}

func cachedTrafficBySeries(ctx context.Context, now time.Time) (map[string][][2]float64, error) {
	pulseTraffic.mu.Lock()
	defer pulseTraffic.mu.Unlock()
	if pulseTraffic.series != nil && now.Sub(pulseTraffic.at) < pulseTTL {
		return pulseTraffic.series, nil
	}
	q := fmt.Sprintf(`sum by (service) (rate(traefik_service_requests_total[%s]))`, rateWindow(pulseStep))
	s, err := promQueryRangeBySeries(ctx, q, "service", now.Add(-pulseRange), now, pulseStep)
	if err != nil {
		return nil, err
	}
	pulseTraffic.series, pulseTraffic.at = s, now
	return s, nil
}

// Pulse is GET /api/projects/pulse.
func (h *ProjectsHandler) Pulse(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	all, err := h.Svc.List(ctx)
	if err != nil {
		h.fail(w, "projects pulse", err)
		return
	}
	visible, ok := h.filterProjectsForCaller(ctx, w, all)
	if !ok {
		return
	}
	now := time.Now().UTC()
	out := projectsPulseResponse{
		Since:       now.Add(-pulseRange),
		StepSeconds: int((10 * time.Minute).Seconds()),
		Requests:    [][2]float64{},
		Deploys:     []pulseDeploy{},
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
		envProject := map[string]string{}
		for ns := range namespaces {
			envs, lerr := h.Kube.ListKusoEnvironments(ctx, ns)
			if lerr != nil {
				h.Logger.Warn("projects pulse: list envs", "namespace", ns, "err", lerr)
				continue
			}
			for i := range envs {
				if projects[envs[i].Spec.Project] {
					envProject[ns+"-"+envs[i].Name] = envs[i].Spec.Project
				}
			}
		}
		series, perr := cachedTrafficBySeries(ctx, now)
		if perr != nil {
			h.Logger.Warn("projects pulse: prometheus", "err", perr)
		} else {
			out.Requests = sumVisibleTraffic(series, envProject)
			out.RequestsAvailable = true
		}
	}

	if h.DB != nil {
		recs, derr := h.DB.ListSucceededBuildRecordsSince(ctx, out.Since)
		if derr != nil {
			h.Logger.Warn("projects pulse: build records", "err", derr)
		}
		for _, rec := range recs {
			if projects[rec.Project] {
				out.Deploys = append(out.Deploys, pulseDeploy{Project: rec.Project, Service: rec.Service, At: rec.CreatedAt.UTC()})
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// traefikServiceEnv turns Traefik's service label,
// "<namespace>-<env>-<port>@kubernetes", into "<namespace>-<env>".
func traefikServiceEnv(label string) (string, bool) {
	base, ok := strings.CutSuffix(label, "@kubernetes")
	if !ok {
		return "", false
	}
	i := strings.LastIndexByte(base, '-')
	if i <= 0 {
		return "", false
	}
	return base[:i], true
}

// sumVisibleTraffic adds up the series whose env belongs to a visible
// project and converts requests/s to requests/min.
func sumVisibleTraffic(series map[string][][2]float64, envProject map[string]string) [][2]float64 {
	totals := map[float64]float64{}
	for label, points := range series {
		env, ok := traefikServiceEnv(label)
		if !ok {
			continue
		}
		if _, mine := envProject[env]; !mine {
			continue
		}
		for _, p := range points {
			totals[p[0]] += p[1] * 60
		}
	}
	out := make([][2]float64, 0, len(totals))
	for t, v := range totals {
		out = append(out, [2]float64{t, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

// promQueryRangeBySeries runs a query_range and returns every series'
// points keyed by the value of one label.
func promQueryRangeBySeries(ctx context.Context, query, label string, start, end time.Time, step string) (map[string][][2]float64, error) {
	base := promBaseURL
	if v := os.Getenv("KUSO_PROMETHEUS_URL"); v != "" {
		base = v
	}
	q := url.Values{}
	q.Set("query", query)
	q.Set("start", strconv.FormatInt(start.Unix(), 10))
	q.Set("end", strconv.FormatInt(end.Unix(), 10))
	q.Set("step", step)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/query_range?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := promHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prometheus: status %d", resp.StatusCode)
	}
	var body struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Values [][]any           `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	if body.Status != "success" {
		return nil, fmt.Errorf("prometheus: status %q", body.Status)
	}
	out := make(map[string][][2]float64, len(body.Data.Result))
	for _, r := range body.Data.Result {
		key := r.Metric[label]
		pts := make([][2]float64, 0, len(r.Values))
		for _, p := range r.Values {
			if len(p) != 2 {
				continue
			}
			t, _ := p[0].(float64)
			s, _ := p[1].(string)
			v, err := strconv.ParseFloat(s, 64)
			if err != nil || v != v || v > 1e308 || v < -1e308 {
				continue
			}
			pts = append(pts, [2]float64{t, v})
		}
		out[key] = append(out[key], pts...)
	}
	return out, nil
}
