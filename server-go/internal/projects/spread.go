package projects

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Spread policy values stamped onto KusoEnvironment.spec.spreadPolicy.
const (
	spreadHard = "hard" // DoNotSchedule — replicas guaranteed on distinct nodes
	spreadSoft = "soft" // ScheduleAnyway — best-effort (single-node safe)
)

// resolveSpreadPolicy decides whether multi-replica pods should be
// forced onto distinct nodes ("hard") or merely prefer to ("soft"),
// based on the live count of SCHEDULABLE nodes:
//
//   - >1 schedulable node → "hard": a node reboot can't take all
//     replicas down, and nothing hangs Pending (there's somewhere for
//     each replica to go).
//   - ≤1 schedulable node → "soft": a hard constraint would strand the
//     2nd replica Pending forever on a single-node cluster.
//
// Fail-safe: any error reading nodes, or a nil kube client, returns
// "soft" — we never risk wedging scheduling because we couldn't count
// nodes. A node is "schedulable" if it's Ready and not cordoned
// (cordoned during maintenance shouldn't count toward the HA budget).
func (s *Service) resolveSpreadPolicy(ctx context.Context) string {
	if s.Kube == nil || s.Kube.Clientset == nil {
		return spreadSoft
	}
	count := func(nodes []*corev1.Node) int {
		n := 0
		for _, nd := range nodes {
			if nodeSchedulable(nd) {
				n++
			}
		}
		return n
	}
	// Prefer the informer cache (placement validation uses the same).
	if cached, ok := s.Kube.Cache.ListNodes(); ok {
		if count(cached) > 1 {
			return spreadHard
		}
		return spreadSoft
	}
	lctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	nodes, err := s.Kube.Clientset.CoreV1().Nodes().List(lctx, metav1.ListOptions{})
	if err != nil {
		return spreadSoft // fail safe
	}
	schedulable := 0
	for i := range nodes.Items {
		if nodeSchedulable(&nodes.Items[i]) {
			schedulable++
		}
	}
	if schedulable > 1 {
		return spreadHard
	}
	return spreadSoft
}

// nodeSchedulable reports whether a node is Ready and not cordoned.
func nodeSchedulable(n *corev1.Node) bool {
	if n == nil || n.Spec.Unschedulable {
		return false
	}
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}
