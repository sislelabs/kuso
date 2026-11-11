package nodewatch

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// activatorRebalanceEvery bounds how often nodewatch evicts an activator
// replica to spread the pair. If the scheduler puts the replacement back
// on the same node (the other node is full), this caps the churn.
const activatorRebalanceEvery = time.Hour

// rebalanceActivator evicts one kuso-activator replica when every ready
// replica sits on the same node and another schedulable Ready node
// exists. Preferred anti-affinity is only consulted at scheduling time,
// so a single-node install that later grew keeps both replicas on the
// first node forever, and losing that node takes every sleep-routed host
// down with it. The Eviction API honours the activator's PDB
// (minAvailable 1), so the other replica keeps serving.
func (w *Watcher) rebalanceActivator(ctx context.Context, nodes []*corev1.Node, now time.Time) {
	w.mu.Lock()
	recent := !w.lastActivatorRebalance.IsZero() && now.Sub(w.lastActivatorRebalance) < activatorRebalanceEvery
	w.mu.Unlock()
	if recent {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	list, err := w.Kube.Clientset.CoreV1().Pods(activatorNamespace).List(cctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/name=kuso-activator",
	})
	if err != nil {
		w.Logger.Debug("nodewatch: list activator pods", "err", err)
		return
	}
	victim := colocatedVictim(list.Items, nodes)
	if victim == nil {
		return
	}
	w.mu.Lock()
	w.lastActivatorRebalance = now
	w.mu.Unlock()
	err = w.Kube.Clientset.CoreV1().Pods(victim.Namespace).EvictV1(ctx, &policyv1.Eviction{
		ObjectMeta: metav1.ObjectMeta{Name: victim.Name, Namespace: victim.Namespace},
	})
	switch {
	case err == nil:
		w.Logger.Info("nodewatch: evicted a co-located kuso-activator replica so it reschedules on another node",
			"pod", victim.Name, "node", victim.Spec.NodeName)
	case apierrors.IsNotFound(err):
	default:
		// 429 = PDB says no right now; forbidden = RBAC without
		// pods/eviction. Either way the pair stays put until the next window.
		w.Logger.Warn("nodewatch: evict co-located kuso-activator replica", "pod", victim.Name, "err", err)
	}
}

// colocatedVictim returns the newest activator pod to evict, or nil when
// the replicas are already spread, any replica isn't Ready (a rollout or
// a failover is in progress), or no other node could take one.
func colocatedVictim(pods []corev1.Pod, nodes []*corev1.Node) *corev1.Pod {
	var live []*corev1.Pod
	for i := range pods {
		p := &pods[i]
		if p.DeletionTimestamp != nil || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		if p.Spec.NodeName == "" || !podReady(p) {
			return nil
		}
		live = append(live, p)
	}
	if len(live) < 2 {
		return nil
	}
	host := live[0].Spec.NodeName
	for _, p := range live[1:] {
		if p.Spec.NodeName != host {
			return nil
		}
	}
	elsewhere := false
	for _, n := range nodes {
		if n.Name != host && schedulable(n) {
			elsewhere = true
			break
		}
	}
	if !elsewhere {
		return nil
	}
	newest := live[0]
	for _, p := range live[1:] {
		if newest.CreationTimestamp.Before(&p.CreationTimestamp) {
			newest = p
		}
	}
	return newest
}

func podReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// schedulable: Ready, not cordoned, and no hard taint the activator
// doesn't tolerate (it tolerates none).
func schedulable(n *corev1.Node) bool {
	if n.Spec.Unschedulable || !isReady(n) {
		return false
	}
	for _, t := range n.Spec.Taints {
		if t.Effect == corev1.TaintEffectNoSchedule || t.Effect == corev1.TaintEffectNoExecute {
			return false
		}
	}
	return true
}
