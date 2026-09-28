package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"kuso/server/internal/drains"
	"kuso/server/internal/kube"
)

// MetricsExportHandler serves GET /api/metrics/export: kuso's per-env
// series (request rate, 5xx rate, p95 latency from Traefik via the
// in-cluster Prometheus; CPU/memory from metrics-server) in Prometheus
// text format, labelled project/service/env. Point Grafana Alloy, a
// Prometheus scrape job or Grafana Cloud's Metrics Endpoint integration
// at it. Stateless, so every replica can serve it.
type MetricsExportHandler struct {
	Kube      *kube.Client
	Namespace string
	Logger    *slog.Logger
	// Query runs a PromQL instant query and returns value by the
	// Traefik "service" label. Nil uses the in-cluster Prometheus.
	Query func(ctx context.Context, q string) (map[string]float64, error)
}

const (
	exportReqQuery = `sum by (service) (rate(traefik_service_requests_total[5m]))`
	exportErrQuery = `sum by (service) (rate(traefik_service_requests_total{code=~"5.."}[5m]))`
	exportP95Query = `histogram_quantile(0.95, sum by (service, le) (rate(traefik_service_request_duration_seconds_bucket[5m])))`
)

func (h *MetricsExportHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if h.Kube == nil {
		writeErr(w, http.StatusServiceUnavailable, "kube client not configured")
		return
	}
	envs, err := h.envRefs(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "list environments: "+err.Error())
		return
	}
	query := h.Query
	if query == nil {
		query = promQueryByService
	}
	// A missing Prometheus or metrics-server degrades to fewer series,
	// never a failed scrape: resource series still flow without traffic.
	var traffic drains.Traffic
	for _, q := range []struct {
		dst  *map[string]float64
		expr string
	}{{&traffic.Requests, exportReqQuery}, {&traffic.Errors, exportErrQuery}, {&traffic.P95, exportP95Query}} {
		m, err := query(ctx, q.expr)
		if err != nil {
			h.logger().Debug("metrics export: prometheus query", "err", err)
			continue
		}
		*q.dst = m
	}
	res := map[string]drains.Resources{}
	seenNS := map[string]bool{}
	for _, e := range envs {
		if seenNS[e.Namespace] {
			continue
		}
		seenNS[e.Namespace] = true
		items, ok := listPodMetricsCached(ctx, h.Kube, e.Namespace)
		if !ok {
			continue
		}
		for _, it := range items {
			inst := podMetricsInstance(it)
			if inst == "" {
				continue
			}
			rs := res[inst]
			if sumPodMetricsUsage(it, &rs.CPUMilli, &rs.MemBytes) {
				rs.Pods++
			}
			res[inst] = rs
		}
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = drains.WritePrometheus(w, drains.AssembleEnvSamples(envs, traffic, res))
}

func (h *MetricsExportHandler) logger() *slog.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}

// envRefs lists every env with the namespace its workload runs in
// (the project's custom namespace when set).
func (h *MetricsExportHandler) envRefs(ctx context.Context) ([]drains.EnvRef, error) {
	projs, err := h.Kube.ListKusoProjects(ctx, h.Namespace)
	if err != nil {
		return nil, err
	}
	nsOf := map[string]string{}
	for _, p := range projs {
		if p.Spec.Namespace != "" {
			nsOf[p.Name] = p.Spec.Namespace
		}
	}
	envs, err := h.Kube.ListKusoEnvironments(ctx, h.Namespace)
	if err != nil {
		return nil, err
	}
	out := make([]drains.EnvRef, 0, len(envs))
	for _, e := range envs {
		ns := nsOf[e.Spec.Project]
		if ns == "" {
			ns = h.Namespace
		}
		out = append(out, drains.EnvRef{
			Namespace: ns, Name: e.Name, Project: e.Spec.Project,
			Service: drains.ShortService(e.Spec.Project, e.Spec.Service),
		})
	}
	return out, nil
}

// promQueryByService runs an instant query against the in-cluster
// Prometheus and keys each sample by its "service" label.
func promQueryByService(ctx context.Context, q string) (map[string]float64, error) {
	base := promBaseURL
	if v := os.Getenv("KUSO_PROMETHEUS_URL"); v != "" {
		base = v
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/query?"+url.Values{"query": {q}}.Encode(), nil)
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
		Data struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  [2]any            `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	out := map[string]float64{}
	for _, s := range body.Data.Result {
		str, _ := s.Value[1].(string)
		v, err := strconv.ParseFloat(str, 64)
		// NaN (no requests in the window for a histogram) is "no data".
		if err != nil || v != v {
			continue
		}
		out[s.Metric["service"]] = v
	}
	return out, nil
}
