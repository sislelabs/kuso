// Package nodes shapes the cluster-node list the UI consumes. The
// HTTP handler is now a thin adapter that gathers inputs (kube node
// list + pod-counts-per-node + metrics-server usage) and hands them
// to BuildSummaries.
//
// Keeping the transformation here (rather than inline in the
// handler) means it's exercisable in tests without spinning up a
// chi router, and the handler no longer reaches into kube/metrics
// shapes directly.
package nodes

import (
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"

	"kuso/server/internal/kube"
)

// Summary is the slim wire shape the UI consumes. Only kuso-managed
// labels are exposed — the underlying kube/k3s labels stay hidden so
// users don't accidentally edit topology.kubernetes.io/* and break
// the scheduler.
type Summary struct {
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
	// "unreachable" badge instead of a generic "cordoned" one.
	Unreachable bool   `json:"unreachable,omitempty"`
	CreatedAt   string `json:"createdAt,omitempty"`
	// Capacity + live usage. Capacity is what the kubelet reports;
	// Usage is what's actually being consumed (from metrics-server).
	// All values in milli (CPU) or bytes (memory/disk).
	CPUCapacityMilli   int64 `json:"cpuCapacityMilli"`
	CPUUsageMilli      int64 `json:"cpuUsageMilli"`
	MemCapacityBytes   int64 `json:"memCapacityBytes"`
	MemUsageBytes      int64 `json:"memUsageBytes"`
	DiskCapacityBytes  int64 `json:"diskCapacityBytes"`
	DiskAvailableBytes int64 `json:"diskAvailableBytes"`
	Pods               int   `json:"pods"`
	PodsCapacity       int   `json:"podsCapacity"`
}

// Usage is the per-node metrics-server slice the caller passes in.
type Usage struct {
	CPUMilli int64
	MemBytes int64
}

// BuildSummaries shapes a kube node list into the UI's wire format,
// folding in per-node pod counts and metrics-server usage. Pure —
// no kube or HTTP dependencies, fully testable.
//
// podCounts: nodeName → pod count. Missing key = 0.
// usage: nodeName → Usage. Missing key = no live metrics.
//
// Output is sorted by node name so the UI gets a stable order across
// reloads.
func BuildSummaries(items []corev1.Node, podCounts map[string]int, usage map[string]Usage) []Summary {
	out := make([]Summary, 0, len(items))
	for i := range items {
		n := &items[i]
		out = append(out, buildSummary(n, podCounts[n.Name], usage[n.Name]))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func buildSummary(n *corev1.Node, podCount int, u Usage) Summary {
	ready := false
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue {
			ready = true
			break
		}
	}
	region := n.Labels["topology.kubernetes.io/region"]
	if region == "" {
		region = n.Labels[kube.LabelPrefix+"region"]
	}
	zone := n.Labels["topology.kubernetes.io/zone"]
	kusoLabels := map[string]string{}
	for k, v := range n.Labels {
		if len(k) > len(kube.LabelPrefix) && k[:len(kube.LabelPrefix)] == kube.LabelPrefix {
			kusoLabels[k[len(kube.LabelPrefix):]] = v
		}
	}
	// Capacity is what the kubelet self-reports. Allocatable would
	// exclude kube-reserved + system-reserved, but Capacity is what
	// users intuitively expect when they read "16Gi RAM."
	cpuCap := n.Status.Capacity.Cpu().MilliValue()
	memCap, _ := n.Status.Capacity.Memory().AsInt64()
	diskCap, _ := n.Status.Capacity.StorageEphemeral().AsInt64()
	diskAvail, _ := n.Status.Allocatable.StorageEphemeral().AsInt64()
	podsCap, _ := n.Status.Capacity.Pods().AsInt64()
	unreachable := n.Annotations["kuso.sislelabs.com/cordoned-by-nodewatch"] == "true"
	return Summary{
		Name:               n.Name,
		Ready:              ready,
		Roles:              roles(n.Labels),
		Region:             region,
		Zone:               zone,
		KusoLabels:         kusoLabels,
		Schedulable:        !n.Spec.Unschedulable,
		Unreachable:        unreachable,
		CreatedAt:          n.CreationTimestamp.Format(time.RFC3339),
		CPUCapacityMilli:   cpuCap,
		CPUUsageMilli:      u.CPUMilli,
		MemCapacityBytes:   memCap,
		MemUsageBytes:      u.MemBytes,
		DiskCapacityBytes:  diskCap,
		DiskAvailableBytes: diskAvail,
		Pods:               podCount,
		PodsCapacity:       int(podsCap),
	}
}

// roles infers the role names the UI surfaces (control-plane,
// worker, etc.) from the well-known node-role.kubernetes.io/* labels
// k3s and kubeadm both set.
func roles(labels map[string]string) []string {
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
