package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// Read-only views over the cluster nodes: list + per-node history.
// Lifecycle ops (join, validate, remove, label edits) live in
// kubernetes_node_lifecycle.go.

// nodeSummary is the slim wire shape the UI needs. Only kuso-managed
// labels are exposed — the underlying kube/k3s labels stay hidden so
// users don't accidentally edit topology.kubernetes.io/* and break the
// scheduler. Taints aren't surfaced at all; the labels endpoint
// derives them from convention (region → NoSchedule).
type nodeSummary struct {
	Name  string   `json:"name"`
	Ready bool     `json:"ready"`
	Roles []string `json:"roles"`
	// Region/Zone read from the upstream topology labels for display
	// only; not editable through the UI.
	Region string `json:"region,omitempty"`
	Zone   string `json:"zone,omitempty"`
	// KusoLabels is the editable surface — only labels under the
	// kuso.sislelabs.com/ namespace, with the prefix stripped on the
	// way out and re-applied on the way in.
	KusoLabels  map[string]string `json:"kusoLabels"`
	Schedulable bool              `json:"schedulable"`
	// Unreachable is true when nodewatch cordoned this node because
	// it has been NotReady past the threshold. Lets the UI render an
	// "unreachable" badge instead of a generic "cordoned" one — the
	// distinction matters because unreachable nodes auto-recover when
	// they come back, while manually-cordoned nodes don't.
	Unreachable bool   `json:"unreachable,omitempty"`
	CreatedAt   string `json:"createdAt,omitempty"`
	// Capacity + live usage. Capacity is what the kubelet reports
	// the box can do; Usage is what's actually being consumed (from
	// metrics-server). Disk is the ephemeral-storage axis; we don't
	// surface PV usage here because it's per-volume, not per-node.
	// All values in milli (CPU) or bytes (memory/disk) so the UI
	// can format consistently.
	CPUCapacityMilli   int64 `json:"cpuCapacityMilli"`
	CPUUsageMilli      int64 `json:"cpuUsageMilli"`
	MemCapacityBytes   int64 `json:"memCapacityBytes"`
	MemUsageBytes      int64 `json:"memUsageBytes"`
	DiskCapacityBytes  int64 `json:"diskCapacityBytes"`
	DiskAvailableBytes int64 `json:"diskAvailableBytes"`
	Pods               int   `json:"pods"`
	PodsCapacity       int   `json:"podsCapacity"`
}

// Nodes lists every cluster node with the bits the UI needs to show
// region/zone, roles, taint markers, Ready state, and live resource
// usage. Usage data comes from metrics-server via the raw REST API
// — we don't pull in the metrics client-go package because that
// would add ~20MB of vendored deps for a single map lookup.
func (h *KubernetesHandler) Nodes(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := kubeCtx(r)
	defer cancel()
	nodes, err := h.Kube.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		h.Logger.Error("list nodes", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	// Pod count per node — single list call beats N pod-per-node
	// calls. fieldSelector spec.nodeName isn't supported on List
	// without a label index in some k8s versions, so we filter
	// in-memory after a cluster-wide list.
	allPods, _ := h.Kube.Clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	podsByNode := map[string]int{}
	if allPods != nil {
		for _, p := range allPods.Items {
			podsByNode[p.Spec.NodeName]++
		}
	}
	// Live CPU/memory from metrics-server. Failure is non-fatal —
	// metrics-server is optional in some clusters; we just leave
	// usage fields at 0 when it's unavailable.
	usage := nodeMetrics(ctx, h.Kube)
	out := make([]nodeSummary, 0, len(nodes.Items))
	for i := range nodes.Items {
		n := &nodes.Items[i]
		ready := false
		for _, c := range n.Status.Conditions {
			if c.Type == "Ready" && c.Status == "True" {
				ready = true
				break
			}
		}
		roles := nodeRoles(n.Labels)
		region := n.Labels["topology.kubernetes.io/region"]
		if region == "" {
			region = n.Labels[kusoLabelPrefix+"region"]
		}
		zone := n.Labels["topology.kubernetes.io/zone"]
		kusoLabels := map[string]string{}
		for k, v := range n.Labels {
			if len(k) > len(kusoLabelPrefix) && k[:len(kusoLabelPrefix)] == kusoLabelPrefix {
				kusoLabels[k[len(kusoLabelPrefix):]] = v
			}
		}
		// Capacity is what the kubelet self-reports. Allocatable
		// would be more accurate (excludes kube-reserved + system-
		// reserved), but Capacity is what users intuitively expect
		// when they read "16Gi RAM."
		cpuCap := n.Status.Capacity.Cpu().MilliValue()
		memCap, _ := n.Status.Capacity.Memory().AsInt64()
		diskCap, _ := n.Status.Capacity.StorageEphemeral().AsInt64()
		diskAvail, _ := n.Status.Allocatable.StorageEphemeral().AsInt64()
		podsCap, _ := n.Status.Capacity.Pods().AsInt64()
		var cpuUse, memUse int64
		if u, ok := usage[n.Name]; ok {
			cpuUse = u.cpuMilli
			memUse = u.memBytes
		}
		unreachable := n.Annotations["kuso.sislelabs.com/cordoned-by-nodewatch"] == "true"
		out = append(out, nodeSummary{
			Name:               n.Name,
			Ready:              ready,
			Roles:              roles,
			Region:             region,
			Zone:               zone,
			KusoLabels:         kusoLabels,
			Schedulable:        !n.Spec.Unschedulable,
			Unreachable:        unreachable,
			CreatedAt:          n.CreationTimestamp.Format(time.RFC3339),
			CPUCapacityMilli:   cpuCap,
			CPUUsageMilli:      cpuUse,
			MemCapacityBytes:   memCap,
			MemUsageBytes:      memUse,
			DiskCapacityBytes:  diskCap,
			DiskAvailableBytes: diskAvail,
			Pods:               podsByNode[n.Name],
			PodsCapacity:       int(podsCap),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, out)
}

// NodeHistory returns up-to-7-days of resource samples for a node so
// the UI can render CPU/RAM/Disk sparklines on the drill-down. The
// sampler goroutine writes one row per node per 30 min — see
// internal/nodemetrics. Empty array is a valid response (sampler
// hasn't ticked yet, or this node was just added).
func (h *KubernetesHandler) NodeHistory(w http.ResponseWriter, r *http.Request) {
	if h.DB == nil {
		http.Error(w, "metrics history not wired", http.StatusServiceUnavailable)
		return
	}
	name := chi.URLParam(r, "name")
	if name == "" {
		http.Error(w, "node name required", http.StatusBadRequest)
		return
	}
	// `since` defaults to 7 days; cap any user-supplied window at 7d
	// so we don't accidentally serve a denial-of-service-grade query.
	hours := 24 * 7
	if q := r.URL.Query().Get("hours"); q != "" {
		if v, err := strconv.Atoi(q); err == nil && v > 0 && v <= 24*7 {
			hours = v
		}
	}
	ctx, cancel := kubeCtx(r)
	defer cancel()
	rows, err := h.DB.ListNodeMetrics(ctx, name, time.Now().Add(-time.Duration(hours)*time.Hour))
	if err != nil {
		h.Logger.Error("node history", "node", name, "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"node":    name,
		"samples": rows,
	})
}

// nodeUsage is the metrics-server slice we keep per node.
type nodeUsage struct {
	cpuMilli int64
	memBytes int64
}

// nodeMetrics fetches metrics.k8s.io/v1beta1/nodes via the discovery
// REST client. Returns name → usage. Empty map on any failure —
// cluster monitoring shouldn't be a hard dependency for the nodes
// list (metrics-server is optional in some k3s installs).
func nodeMetrics(ctx context.Context, kube *kube.Client) map[string]nodeUsage {
	out := map[string]nodeUsage{}
	if kube == nil || kube.Clientset == nil {
		return out
	}
	rest := kube.Clientset.Discovery().RESTClient()
	if rest == nil {
		return out
	}
	mctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	body, err := rest.Get().AbsPath("/apis/metrics.k8s.io/v1beta1/nodes").DoRaw(mctx)
	if err != nil {
		return out
	}
	var resp struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Usage struct {
				CPU    string `json:"cpu"`    // "<n>n" or "<n>m" — we coerce to milli-CPU
				Memory string `json:"memory"` // "<n>Ki|Mi|Gi"
			} `json:"usage"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return out
	}
	for _, it := range resp.Items {
		out[it.Metadata.Name] = nodeUsage{
			cpuMilli: parseCPU(it.Usage.CPU),
			memBytes: parseQuantity(it.Usage.Memory),
		}
	}
	return out
}

// nodeRoles infers the role names the UI surfaces (control-plane,
// worker, etc.) from the well-known kubernetes.io/role/* labels k3s and
// kubeadm both set.
func nodeRoles(labels map[string]string) []string {
	const prefix = "node-role.kubernetes.io/"
	out := []string{}
	for k := range labels {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			out = append(out, k[len(prefix):])
		}
	}
	if len(out) == 0 {
		out = append(out, "worker")
	}
	sort.Strings(out)
	return out
}
