package nodemetrics

import (
	"context"
	"errors"
	"log/slog"

	corev1 "k8s.io/api/core/v1"

	"kuso/server/internal/kube"
)

// diskStats fetches real per-node disk usage from each kubelet's
// /stats/summary (see kube.NodeFilesystems for why not the node object).
//
// Best-effort, exactly like metricsServerUsage: a node that fails to
// answer is left out of the map and its row carries no disk figures.
// Failures are summarised in one WARN per tick; a silent `continue`
// once hid a week of blank disk metrics. While a 403 has the calls
// parked, nothing is re-logged.
func (s *Sampler) diskStats(ctx context.Context, nodes []*corev1.Node) map[string]kube.NodeFS {
	out, errs := s.Kube.NodeFilesystems(ctx, nodes)
	if len(errs) == 0 || (len(errs) == 1 && errs[0] == kube.ErrKubeletForbidden) {
		return out
	}
	logger := s.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if errors.Is(errs[0], kube.ErrKubeletForbidden) {
		logger.Warn("nodemetrics: kubelet stats forbidden (kuso-server needs nodes/stats get); disk figures left blank",
			"failedNodes", len(errs), "nodes", len(nodes), "firstErr", errs[0])
		return out
	}
	logger.Warn("nodemetrics: kubelet disk summary unavailable; disk figures left blank",
		"failedNodes", len(errs), "nodes", len(nodes), "firstErr", errs[0])
	return out
}
