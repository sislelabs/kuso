// Package handlers — kubernetes.go is the orchestrator for the
// /api/kubernetes/* surface. It owns the handler struct, the route
// table, and a small set of shared utilities. The actual route
// implementations live in sibling files:
//
//   - kubernetes_cluster.go       — Events, StorageClasses, Domains
//   - kubernetes_nodes.go         — Nodes (list), NodeHistory
//   - kubernetes_node_lifecycle.go — Join/Validate/Remove + label PUT
//   - kubernetes_env_metrics.go   — EnvMetrics + EnvTimeseries
//
// All four files share the package and re-use parseCPU/parseQuantity
// + the kusoLabelPrefix constant defined here.
package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/db"
	"kuso/server/internal/kube"
)

// KubernetesHandler exposes /api/kubernetes/* — events, storage classes,
// the domains the cluster already advertises via Ingress, plus per-node
// + per-env telemetry. Concrete handlers live in the kubernetes_*.go
// siblings; this file just holds the wiring.
type KubernetesHandler struct {
	Kube      *kube.Client
	Namespace string
	DB        *db.DB
	Logger    *slog.Logger
}

// Mount registers the routes onto the bearer-protected router.
func (h *KubernetesHandler) Mount(rt interface {
	Get(string, http.HandlerFunc)
	Post(string, http.HandlerFunc)
	Patch(string, http.HandlerFunc)
	Put(string, http.HandlerFunc)
}) {
	rt.Get("/api/kubernetes/events", h.Events)
	rt.Get("/api/kubernetes/storageclasses", h.StorageClasses)
	rt.Get("/api/kubernetes/domains", h.Domains)
	rt.Get("/api/kubernetes/nodes", h.Nodes)
	// 7-day history for the sparkline drill-down on /settings/nodes.
	// Backed by the SQLite NodeMetric table populated by the sampler
	// goroutine — point-in-time data lives on the Nodes() endpoint.
	rt.Get("/api/kubernetes/nodes/{name}/history", h.NodeHistory)
	// Single endpoint, simple semantics: replace the kuso labels for
	// this node with the body. Server expands kuso conventions (region
	// → matching NoSchedule taint) under the hood.
	rt.Put("/api/kubernetes/nodes/{name}/labels", h.PutNodeLabels)
	// Add a node: SSH into a remote VM, run k3s agent install. The
	// k3s server token is read from the hostPath mount the deploy yaml
	// puts at /etc/kuso/k3s-token (control-plane-pinned pod).
	rt.Post("/api/kubernetes/nodes/join", h.JoinNode)
	// Pre-flight check before Join. Same body as join; opens an SSH
	// session, runs a series of probes, returns per-check pass/fail.
	// Coolify-style: the operator clicks Validate first, fixes any
	// missing prereqs, THEN clicks Join.
	rt.Post("/api/kubernetes/nodes/validate", h.ValidateNode)
	// Remove: cordon → drain → kubectl delete node → optional ssh
	// uninstall. Body carries the same Credentials shape so we can
	// also clean up the host. Without creds we just untrack from the
	// kube control plane (the node continues to exist as a dead VM).
	rt.Post("/api/kubernetes/nodes/{name}/remove", h.RemoveNode)
	// Per-env CPU + mem snapshot. Reads from the metrics.k8s.io
	// metrics-server API. Returns one entry per pod in the env.
	rt.Get("/api/kubernetes/envs/{env}/metrics", h.EnvMetrics)
	// Per-env traffic timeseries (requests/sec, error rate, p95
	// response time). Reads from the in-cluster prometheus deployed
	// via deploy/prometheus.yaml. The host can reach kuso-prometheus
	// at the cluster-local DNS name.
	rt.Get("/api/kubernetes/envs/{env}/timeseries", h.EnvTimeseries)
}

// kusoLabelPrefix is the namespace every kuso-managed node label lives
// under. Sites read+write this directly; centralising the const makes
// renaming cheap if we ever need to.
const kusoLabelPrefix = "kuso.sislelabs.com/"

// kubeCtx returns a 10-second deadline context for kube round-trips.
// Long enough to ride out a slow apiserver tick, short enough that a
// wedged handler doesn't pin a goroutine forever.
func kubeCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 10*time.Second)
}

// chiURLParam pulls a path param off the chi context. Wrapped so we
// don't dot-import chi from every handler.
func chiURLParam(r *http.Request, name string) string {
	return chi.URLParam(r, name)
}

// decodeJSON pulls the request body into out, capping at 1 MiB to
// avoid unbounded reads from a hostile client.
func decodeJSON(r *http.Request, out any) error {
	body := http.MaxBytesReader(nil, r.Body, 1<<20)
	defer body.Close()
	return json.NewDecoder(body).Decode(out)
}

// parseCPU returns CPU in millicores. Accepts kube quantity formats
// like "100m", "0.5", "1". Anything else returns 0 (the panel just
// shows "—"). Used by both the env-metrics path and the per-node
// metrics-server slice.
func parseCPU(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if strings.HasSuffix(s, "n") {
		// nanocores → millicores
		n, _ := strconv.ParseInt(strings.TrimSuffix(s, "n"), 10, 64)
		return n / 1_000_000
	}
	if strings.HasSuffix(s, "u") {
		// microcores
		n, _ := strconv.ParseInt(strings.TrimSuffix(s, "u"), 10, 64)
		return n / 1_000
	}
	if strings.HasSuffix(s, "m") {
		n, _ := strconv.ParseInt(strings.TrimSuffix(s, "m"), 10, 64)
		return n
	}
	// Treat as whole cores.
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return int64(f * 1000)
	}
	return 0
}

// parseQuantity returns bytes from a kube quantity string. Handles
// the common Ki/Mi/Gi suffixes plus plain bytes. Anything else → 0.
func parseQuantity(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	mults := []struct {
		suf string
		mul int64
	}{
		{"Ei", 1 << 60}, {"Pi", 1 << 50}, {"Ti", 1 << 40},
		{"Gi", 1 << 30}, {"Mi", 1 << 20}, {"Ki", 1 << 10},
		{"E", 1_000_000_000_000_000_000}, {"P", 1_000_000_000_000_000},
		{"T", 1_000_000_000_000}, {"G", 1_000_000_000},
		{"M", 1_000_000}, {"k", 1_000},
	}
	for _, m := range mults {
		if strings.HasSuffix(s, m.suf) {
			n, _ := strconv.ParseInt(strings.TrimSuffix(s, m.suf), 10, 64)
			return n * m.mul
		}
	}
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}
