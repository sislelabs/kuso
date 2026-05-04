package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Per-env runtime telemetry: pod-level CPU/RAM snapshots from
// metrics-server, traffic + latency timeseries proxied off the
// in-cluster Prometheus. Both surfaces tolerate missing backends —
// empty list / empty series rather than 5xx — so the panel renders a
// clean "no data yet" state instead of an error banner.

// promBaseURL is the in-cluster Prometheus endpoint. Override via
// KUSO_PROMETHEUS_URL when running outside the cluster (tests, dev).
const promBaseURL = "http://kuso-prometheus.kuso.svc.cluster.local:9090"

// promHTTPClient is a small client we reuse for PromQL proxying. The
// in-cluster prometheus is on the same network — short timeout is
// fine and keeps misbehaving prom from wedging the kuso server.
var promHTTPClient = &http.Client{Timeout: 10 * time.Second}

// envMetricsResponse is the JSON wire shape: a list of per-pod
// resource snapshots plus the timestamp the metrics-server emitted.
type envMetricsResponse struct {
	Env    string         `json:"env"`
	Window string         `json:"window,omitempty"`
	Pods   []podMetricRow `json:"pods"`
}

type podMetricRow struct {
	Pod       string `json:"pod"`
	Timestamp string `json:"timestamp,omitempty"`
	CPUm      int64  `json:"cpuMillicores"`
	MemBytes  int64  `json:"memBytes"`
}

// EnvMetrics returns the current CPU + memory usage for every pod in
// the named env. Sources from metrics.k8s.io/v1beta1 PodMetrics via
// the dynamic client (avoids pulling in k8s.io/metrics as a separate
// module dep). Returns an empty list when metrics-server isn't
// installed or the env has no pods yet.
func (h *KubernetesHandler) EnvMetrics(w http.ResponseWriter, r *http.Request) {
	envName := chi.URLParam(r, "env")
	if envName == "" {
		http.Error(w, "missing env", http.StatusBadRequest)
		return
	}
	ctx, cancel := kubeCtx(r)
	defer cancel()

	gvr := schema.GroupVersionResource{
		Group:    "metrics.k8s.io",
		Version:  "v1beta1",
		Resource: "pods",
	}
	list, err := h.Kube.Dynamic.Resource(gvr).Namespace(h.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/instance=" + envName,
	})
	if err != nil {
		// metrics-server not installed → return empty so the UI shows
		// a "no metrics yet" state rather than an error banner.
		writeJSON(w, http.StatusOK, envMetricsResponse{Env: envName, Pods: []podMetricRow{}})
		return
	}
	out := envMetricsResponse{Env: envName, Pods: make([]podMetricRow, 0, len(list.Items))}
	for i := range list.Items {
		item := list.Items[i].Object
		row := podMetricRow{Pod: list.Items[i].GetName()}
		if ts, ok := item["timestamp"].(string); ok {
			row.Timestamp = ts
		}
		if win, ok := item["window"].(string); ok && out.Window == "" {
			out.Window = win
		}
		// Sum across containers in the pod. The metrics-server
		// emits one entry per container under .containers[].usage.
		if containers, ok := item["containers"].([]any); ok {
			for _, c := range containers {
				cm, ok := c.(map[string]any)
				if !ok {
					continue
				}
				usage, ok := cm["usage"].(map[string]any)
				if !ok {
					continue
				}
				if cpu, ok := usage["cpu"].(string); ok {
					row.CPUm += parseCPU(cpu)
				}
				if mem, ok := usage["memory"].(string); ok {
					row.MemBytes += parseQuantity(mem)
				}
			}
		}
		out.Pods = append(out.Pods, row)
	}
	writeJSON(w, http.StatusOK, out)
}

// timeseriesResponse mirrors the PromQL `query_range` shape, simplified
// for the kuso UI: one series per metric, points are [unixSeconds, value].
type timeseriesResponse struct {
	Env    string                  `json:"env"`
	Range  string                  `json:"range"`
	Step   string                  `json:"step"`
	Series map[string][][2]float64 `json:"series"` // metric name → [t, v] points
}

// EnvTimeseries returns request rate / error rate / p95 latency for
// the env over a given range (e.g. range=1h, step=30s). Backed by
// PromQL queries against the in-cluster prometheus.
func (h *KubernetesHandler) EnvTimeseries(w http.ResponseWriter, r *http.Request) {
	envName := chi.URLParam(r, "env")
	if envName == "" {
		http.Error(w, "missing env", http.StatusBadRequest)
		return
	}
	rangeStr := r.URL.Query().Get("range")
	if rangeStr == "" {
		rangeStr = "1h"
	}
	stepStr := r.URL.Query().Get("step")
	if stepStr == "" {
		stepStr = pickStep(rangeStr)
	}
	dur, err := time.ParseDuration(rangeStr)
	if err != nil || dur <= 0 || dur > 30*24*time.Hour {
		http.Error(w, "bad range", http.StatusBadRequest)
		return
	}
	end := time.Now().UTC()
	start := end.Add(-dur)

	// Service-name selector for traefik. Actual label format is
	//   service="<namespace>-<envname>-http@kubernetes"
	// (with namespace prefix and -http suffix for the typical http
	// backend). The Ingress's underlying k8s Service is named after
	// the env CR. We grab the namespace from h.Namespace; everything
	// in kuso lives in one ns by default.
	prefix := h.Namespace + "-" + envName
	matcher := escapePromLabel(prefix) + ".*@kubernetes"

	queries := map[string]string{
		// requests/s — sum across all status codes & methods
		"requests": fmt.Sprintf(
			`sum(rate(traefik_service_requests_total{service=~"%s"}[1m]))`, matcher),
		// 5xx error rate as a fraction of all requests
		"errors": fmt.Sprintf(
			`(sum(rate(traefik_service_requests_total{service=~"%s",code=~"5.."}[1m])) or vector(0)) `+
				`/ clamp_min(sum(rate(traefik_service_requests_total{service=~"%s"}[1m])), 1)`,
			matcher, matcher),
		// p95 latency in ms
		"p95ms": fmt.Sprintf(
			`1000 * histogram_quantile(0.95, sum by (le) (rate(traefik_service_request_duration_seconds_bucket{service=~"%s"}[5m])))`,
			matcher),
	}

	out := timeseriesResponse{
		Env:    envName,
		Range:  rangeStr,
		Step:   stepStr,
		Series: map[string][][2]float64{},
	}

	ctx, cancel := kubeCtx(r)
	defer cancel()

	for name, q := range queries {
		points, perr := promQueryRange(ctx, q, start, end, stepStr)
		if perr != nil {
			// Don't fail the whole response — return what we have and
			// let the panel fall back to an empty series. Common case
			// is "no data yet" which the UI renders as a flat line.
			out.Series[name] = [][2]float64{}
			continue
		}
		out.Series[name] = points
	}
	writeJSON(w, http.StatusOK, out)
}

// pickStep chooses a reasonable scrape step for the range so the
// resulting series has roughly 60–240 points. Saves the client from
// having to compute this and keeps PromQL responses small.
func pickStep(rangeStr string) string {
	d, err := time.ParseDuration(rangeStr)
	if err != nil {
		return "30s"
	}
	switch {
	case d <= 1*time.Hour:
		return "30s"
	case d <= 6*time.Hour:
		return "2m"
	case d <= 24*time.Hour:
		return "10m"
	case d <= 7*24*time.Hour:
		return "1h"
	default:
		return "6h"
	}
}

// escapePromLabel escapes characters that have meaning inside a PromQL
// label-matcher value. We only need to handle backslashes and double
// quotes since the matcher is wrapped in `"..."`.
func escapePromLabel(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}

// promQueryRange executes a PromQL `query_range` and returns the
// first series' points as [unixSeconds, value] tuples. Multiple
// series get summed by the query (we always sum() before returning).
func promQueryRange(ctx context.Context, query string, start, end time.Time, step string) ([][2]float64, error) {
	base := promBaseURL
	if v := os.Getenv("KUSO_PROMETHEUS_URL"); v != "" {
		base = v
	}
	q := url.Values{}
	q.Set("query", query)
	q.Set("start", strconv.FormatInt(start.Unix(), 10))
	q.Set("end", strconv.FormatInt(end.Unix(), 10))
	q.Set("step", step)
	u := base + "/api/v1/query_range?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := promHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("prometheus: status %d", resp.StatusCode)
	}
	var body struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Values [][]any `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	if body.Status != "success" || len(body.Data.Result) == 0 {
		return [][2]float64{}, nil
	}
	raw := body.Data.Result[0].Values
	out := make([][2]float64, 0, len(raw))
	for _, p := range raw {
		if len(p) != 2 {
			continue
		}
		t, _ := p[0].(float64)
		vstr, _ := p[1].(string)
		// Prom encodes NaN/+Inf/-Inf as the literal strings "NaN",
		// "+Inf", "-Inf". Go's json.Encode rejects these floats and
		// silently fails the whole response (content-length: 0,
		// status 200, no body). Skip them — for the UI a missing
		// point is identical to "no data" anyway.
		if vstr == "NaN" || vstr == "+Inf" || vstr == "-Inf" {
			continue
		}
		v, err := strconv.ParseFloat(vstr, 64)
		if err != nil {
			continue
		}
		// Defensive: even valid parse can land on NaN if Prom sends
		// e.g. "nan" lowercase. Skip those too.
		if v != v || v > 1e308 || v < -1e308 {
			continue
		}
		out = append(out, [2]float64{t, v})
	}
	return out, nil
}
