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

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"

	"k8s.io/apimachinery/pkg/api/resource"

	"kuso/server/internal/hostcheck"
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
	UsageSince   *time.Time            `json:"usageSince,omitempty"`
	Services     []insightsService     `json:"services"`
	Deploys      []insightsDeploy      `json:"deploys"`
	FailedBuilds []insightsFailedBuild `json:"failedBuilds"`
	// CertProblems are certificates for the caller's hosts that are not
	// Ready, expired, or expire within certWarnDays.
	CertProblems []insightsCertProblem `json:"certProblems"`
}

// insightsResources is one pod's requests and limits; zero is unset.
type insightsResources struct {
	CPURequestMilli int64 `json:"cpuRequestMilli,omitempty"`
	CPULimitMilli   int64 `json:"cpuLimitMilli,omitempty"`
	MemRequestBytes int64 `json:"memRequestBytes,omitempty"`
	MemLimitBytes   int64 `json:"memLimitBytes,omitempty"`
}

type insightsFailedBuild struct {
	Project       string    `json:"project"`
	Service       string    `json:"service"`
	At            time.Time `json:"at"`
	Status        string    `json:"status"`
	CommitMessage string    `json:"commitMessage"`
	Error         string    `json:"error"`
}

type insightsCertProblem struct {
	Project string `json:"project"`
	Host    string `json:"host"`
	Problem string `json:"problem"`
}

const (
	certWarnDays    = 14
	maxFailedBuilds = 20
)

type insightsService struct {
	Project   string       `json:"project"`
	Service   string       `json:"service"`
	Env       string       `json:"env"`
	Requests  [][2]float64 `json:"requests"`
	CPU       [][2]float64 `json:"cpu"`
	Memory    [][2]float64 `json:"memory"`
	P95       [][2]float64 `json:"p95"`
	ErrorRate [][2]float64 `json:"errorRate"`
	// Pods is the replica count at the latest usage sample.
	Pods      int                `json:"pods"`
	Resources *insightsResources `json:"resources,omitempty"`
}

type insightsDeploy struct {
	Project       string    `json:"project"`
	Service       string    `json:"service"`
	At            time.Time `json:"at"`
	CommitSha     string    `json:"commitSha"`
	CommitMessage string    `json:"commitMessage"`
}

// insightsTraffic holds the three Traefik range queries for one range
// preset, shared by every caller.
type insightsTraffic struct {
	req, errs, p95 map[string][][2]float64
}

// insightsStale is how old a cached result may be and still be served
// at once while a background refresh runs. The p95 histogram query
// alone takes seconds, so page loads shouldn't wait on it.
const insightsStale = 15 * time.Minute

type insightsEntry struct {
	at         time.Time
	v          insightsTraffic
	refreshing bool
}

var insightsCache struct {
	mu sync.Mutex
	m  map[string]*insightsEntry
	sf singleflight.Group
}

// insightsFetch runs the queries; a var so tests can stub Prometheus.
var insightsFetch = fetchInsightsTraffic

func fetchInsightsTraffic(ctx context.Context, rg insightsRange, now time.Time) (insightsTraffic, error) {
	step := rg.step.String()
	win := rateWindow(step)
	start := now.Add(-rg.dur)
	var out insightsTraffic
	g, gctx := errgroup.WithContext(ctx)
	run := func(dst *map[string][][2]float64, q string) {
		g.Go(func() error {
			m, err := promQueryRangeBySeries(gctx, q, "service", start, now, step)
			*dst = m
			return err
		})
	}
	run(&out.req, fmt.Sprintf(`sum by (service) (rate(traefik_service_requests_total[%s]))`, win))
	run(&out.errs, fmt.Sprintf(`sum by (service) (rate(traefik_service_requests_total{code=~"5.."}[%s]))`, win))
	run(&out.p95, fmt.Sprintf(`1000 * histogram_quantile(0.95, sum by (service, le) (rate(traefik_service_request_duration_seconds_bucket[%s])))`, win))
	return out, g.Wait()
}

// cachedInsightsTraffic returns a fresh result, or a stale one (kicking
// off a background refresh), or waits for one fetch shared by every
// concurrent caller.
func cachedInsightsTraffic(ctx context.Context, key string, rg insightsRange, now time.Time) (insightsTraffic, error) {
	insightsCache.mu.Lock()
	if insightsCache.m == nil {
		insightsCache.m = map[string]*insightsEntry{}
	}
	if e := insightsCache.m[key]; e != nil {
		age := now.Sub(e.at)
		if age < pulseTTL {
			insightsCache.mu.Unlock()
			return e.v, nil
		}
		if age < insightsStale {
			if !e.refreshing {
				e.refreshing = true
				go func() {
					bg, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					_, _ = loadInsightsTraffic(bg, key, rg)
				}()
			}
			v := e.v
			insightsCache.mu.Unlock()
			return v, nil
		}
	}
	insightsCache.mu.Unlock()
	return loadInsightsTraffic(ctx, key, rg)
}

func loadInsightsTraffic(ctx context.Context, key string, rg insightsRange) (insightsTraffic, error) {
	v, err, _ := insightsCache.sf.Do(key, func() (any, error) {
		now := time.Now().UTC()
		out, err := insightsFetch(ctx, rg, now)
		insightsCache.mu.Lock()
		defer insightsCache.mu.Unlock()
		if e := insightsCache.m[key]; e != nil {
			e.refreshing = false
		}
		if err != nil {
			return insightsTraffic{}, err
		}
		insightsCache.m[key] = &insightsEntry{at: now, v: out}
		return out, nil
	})
	if err != nil {
		return insightsTraffic{}, err
	}
	return v.(insightsTraffic), nil
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
		Range:        key,
		StepSeconds:  int(rg.step.Seconds()),
		Since:        now.Add(-rg.dur),
		Until:        now,
		Services:     []insightsService{},
		Deploys:      []insightsDeploy{},
		FailedBuilds: []insightsFailedBuild{},
		CertProblems: []insightsCertProblem{},
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
		hostProject := map[string]string{}
		for ns := range namespaces {
			envs, lerr := h.Kube.ListKusoEnvironments(ctx, ns)
			if lerr != nil {
				h.Logger.Warn("insights: list envs", "namespace", ns, "err", lerr)
				continue
			}
			for i := range envs {
				e := &envs[i]
				if !projects[e.Spec.Project] {
					continue
				}
				for _, host := range append([]string{e.Spec.Host}, e.Spec.AdditionalHosts...) {
					if host != "" {
						hostProject[strings.ToLower(host)] = e.Spec.Project
					}
				}
				if !isProductionEnv(e) {
					continue
				}
				byEnvKey[ns+"-"+e.Name] = len(out.Services)
				out.Services = append(out.Services, insightsService{
					Project:   e.Spec.Project,
					Service:   strings.TrimPrefix(e.Spec.Service, e.Spec.Project+"-"),
					Env:       e.Name,
					Resources: parseResources(e.Spec.Resources),
				})
			}
		}
		if tr, terr := cachedInsightsTraffic(ctx, key, rg, now); terr != nil {
			h.Logger.Warn("insights: prometheus", "err", terr)
		} else {
			out.RequestsAvailable = true
			fillTraffic(out.Services, byEnvKey, tr)
		}
		if h.Kube.Dynamic != nil {
			for ns := range namespaces {
				certs, cerr := hostcheck.ListCertificatesIn(ctx, h.Kube.Dynamic, ns)
				if cerr != nil {
					continue
				}
				out.CertProblems = append(out.CertProblems, certProblems(certs, hostProject, now)...)
			}
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
		if n := len(hist.Samples); n > 0 {
			s.Pods = hist.Samples[n-1].Pods
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
		for _, status := range []string{"failed", "release-failed"} {
			recs, ferr := h.DB.ListBuildRecordsSince(ctx, out.Since, status)
			if ferr != nil {
				h.Logger.Warn("insights: failed builds", "err", ferr)
			}
			for _, rec := range recs {
				if projects[rec.Project] {
					out.FailedBuilds = append(out.FailedBuilds, insightsFailedBuild{
						Project:       rec.Project,
						Service:       strings.TrimPrefix(rec.Service, rec.Project+"-"),
						At:            rec.CreatedAt.UTC(),
						Status:        rec.Status,
						CommitMessage: firstLine(rec.CommitMessage),
						Error:         firstLine(rec.ErrorMessage),
					})
				}
			}
		}
		sort.Slice(out.FailedBuilds, func(i, j int) bool { return out.FailedBuilds[i].At.After(out.FailedBuilds[j].At) })
		if len(out.FailedBuilds) > maxFailedBuilds {
			out.FailedBuilds = out.FailedBuilds[:maxFailedBuilds]
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

// certProblems keeps the certificates that cover one of the caller's
// hosts and have something wrong with them, one row per host.
func certProblems(certs []hostcheck.Cert, hostProject map[string]string, now time.Time) []insightsCertProblem {
	var out []insightsCertProblem
	for _, c := range certs {
		problem, bad := hostcheck.CertProblem(c, now, certWarnDays)
		if !bad {
			continue
		}
		for _, name := range c.DNSNames {
			if p, ok := hostProject[strings.ToLower(name)]; ok {
				out = append(out, insightsCertProblem{Project: p, Host: name, Problem: problem})
			}
		}
	}
	return out
}

// parseResources reads a k8s ResourceRequirements-shaped map. nil when
// nothing is set, so the chart default (no requests or limits) reads as
// unknown rather than zero.
func parseResources(m map[string]any) *insightsResources {
	if len(m) == 0 {
		return nil
	}
	get := func(section, key string, milli bool) int64 {
		sec, _ := m[section].(map[string]any)
		v, ok := sec[key]
		if !ok {
			return 0
		}
		q, err := resource.ParseQuantity(fmt.Sprint(v))
		if err != nil {
			return 0
		}
		if milli {
			return q.MilliValue()
		}
		return q.Value()
	}
	r := &insightsResources{
		CPURequestMilli: get("requests", "cpu", true),
		CPULimitMilli:   get("limits", "cpu", true),
		MemRequestBytes: get("requests", "memory", false),
		MemLimitBytes:   get("limits", "memory", false),
	}
	if *r == (insightsResources{}) {
		return nil
	}
	return r
}

// WarmInsights fills the 24h traffic cache shortly after boot, so the
// first visitor after a release doesn't wait on the p95 query.
func WarmInsights() {
	time.AfterFunc(20*time.Second, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = loadInsightsTraffic(ctx, "24h", insightsRanges["24h"])
	})
}
