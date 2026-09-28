package drains

import (
	"bufio"
	"io"
	"sort"
	"strconv"
	"strings"
)

// EnvRef identifies one kuso environment and the namespace its
// workload runs in.
type EnvRef struct {
	Namespace string
	Name      string
	Project   string
	Service   string
}

// EnvSample is the per-env snapshot served by the metrics export. The
// Has* flags distinguish "no data" (series omitted) from a real zero.
type EnvSample struct {
	Env             EnvRef
	HasTraffic      bool
	RequestsPerSec  float64
	Errors5xxPerSec float64
	HasP95          bool
	P95Seconds      float64
	HasResources    bool
	CPUCores        float64
	MemoryBytes     float64
	Pods            int
}

// MatchTraefikService maps a Traefik service label
// ("<ns>-<env>-<port>@kubernetes") to its env. Env names can prefix one
// another (shop-web vs shop-web-pr-3), so the longest match wins.
func MatchTraefikService(label string, envs []EnvRef) (EnvRef, bool) {
	name, ok := strings.CutSuffix(label, "@kubernetes")
	if !ok {
		return EnvRef{}, false
	}
	var best EnvRef
	bestLen := -1
	for _, e := range envs {
		p := e.Namespace + "-" + e.Name
		if (name == p || strings.HasPrefix(name, p+"-")) && len(p) > bestLen {
			best, bestLen = e, len(p)
		}
	}
	return best, bestLen >= 0
}

type metricDef struct {
	name, help string
	value      func(EnvSample) (float64, bool)
}

var exportedMetrics = []metricDef{
	{"kuso_env_http_requests_per_second", "Request rate over the last 5m, from Traefik.",
		func(s EnvSample) (float64, bool) { return s.RequestsPerSec, s.HasTraffic }},
	{"kuso_env_http_5xx_per_second", "5xx response rate over the last 5m, from Traefik.",
		func(s EnvSample) (float64, bool) { return s.Errors5xxPerSec, s.HasTraffic }},
	{"kuso_env_http_p95_latency_seconds", "p95 request latency over the last 5m, from Traefik.",
		func(s EnvSample) (float64, bool) { return s.P95Seconds, s.HasP95 }},
	{"kuso_env_cpu_cores", "CPU in use across the env's pods, from metrics-server.",
		func(s EnvSample) (float64, bool) { return s.CPUCores, s.HasResources }},
	{"kuso_env_memory_bytes", "Memory in use across the env's pods, from metrics-server.",
		func(s EnvSample) (float64, bool) { return s.MemoryBytes, s.HasResources }},
	{"kuso_env_pods", "Pods reporting metrics for the env.",
		func(s EnvSample) (float64, bool) { return float64(s.Pods), s.HasResources }},
}

// WritePrometheus renders samples in the Prometheus text exposition
// format (0.0.4). Metrics with no samples are omitted entirely.
func WritePrometheus(w io.Writer, samples []EnvSample) error {
	sorted := append([]EnvSample(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool {
		a, b := sorted[i].Env, sorted[j].Env
		if a.Project != b.Project {
			return a.Project < b.Project
		}
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		return a.Name < b.Name
	})
	bw := bufio.NewWriter(w)
	for _, m := range exportedMetrics {
		header := false
		for _, s := range sorted {
			v, ok := m.value(s)
			if !ok {
				continue
			}
			if !header {
				bw.WriteString("# HELP " + m.name + " " + m.help + "\n# TYPE " + m.name + " gauge\n")
				header = true
			}
			bw.WriteString(m.name + `{project="` + escapeLabel(s.Env.Project) + `",service="` + escapeLabel(s.Env.Service) +
				`",env="` + escapeLabel(s.Env.Name) + `"} ` + strconv.FormatFloat(v, 'g', -1, 64) + "\n")
		}
	}
	return bw.Flush()
}

func escapeLabel(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return strings.ReplaceAll(s, "\n", `\n`)
}

// Traffic holds PromQL instant-query results keyed by Traefik's
// service label.
type Traffic struct {
	Requests map[string]float64
	Errors   map[string]float64
	P95      map[string]float64
}

// Resources is metrics-server usage summed across one env's pods.
type Resources struct {
	CPUMilli int64
	MemBytes int64
	Pods     int
}

// AssembleEnvSamples joins traffic (by Traefik service) and resource
// usage (by env name) onto envs. An env with several ports sums its
// rates and reports its worst p95. Envs with no data are omitted.
func AssembleEnvSamples(envs []EnvRef, traffic Traffic, res map[string]Resources) []EnvSample {
	byName := map[string]*EnvSample{}
	get := func(e EnvRef) *EnvSample {
		s := byName[e.Name]
		if s == nil {
			s = &EnvSample{Env: e}
			byName[e.Name] = s
		}
		return s
	}
	for label, v := range traffic.Requests {
		if e, ok := MatchTraefikService(label, envs); ok {
			s := get(e)
			s.HasTraffic = true
			s.RequestsPerSec += v
		}
	}
	for label, v := range traffic.Errors {
		if e, ok := MatchTraefikService(label, envs); ok {
			s := get(e)
			s.HasTraffic = true
			s.Errors5xxPerSec += v
		}
	}
	for label, v := range traffic.P95 {
		if e, ok := MatchTraefikService(label, envs); ok {
			s := get(e)
			if !s.HasP95 || v > s.P95Seconds {
				s.P95Seconds = v
			}
			s.HasP95 = true
		}
	}
	for _, e := range envs {
		r, ok := res[e.Name]
		if !ok || r.Pods == 0 {
			continue
		}
		s := get(e)
		s.HasResources = true
		s.CPUCores = float64(r.CPUMilli) / 1000
		s.MemoryBytes = float64(r.MemBytes)
		s.Pods = r.Pods
	}
	out := make([]EnvSample, 0, len(byName))
	for _, s := range byName {
		out = append(out, *s)
	}
	return out
}
