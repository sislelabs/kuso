package nodewatch

import (
	"context"
	"os"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/pkgupdates"
)

// defaultEvictAfter is how long a node's Ready condition must have been
// Unknown before nodewatch deletes the stateless kuso service pods on it.
// Kubernetes' own taint eviction waits 300s, so a default 1-replica
// service was down at least 5.5 min per dead node.
const defaultEvictAfter = 60 * time.Second

// evictAfter reads KUSO_NODEWATCH_EVICT_AFTER (a Go duration; "0" or
// "off" disables the early eviction and leaves it to Kubernetes).
func evictAfter() time.Duration {
	v := strings.TrimSpace(os.Getenv("KUSO_NODEWATCH_EVICT_AFTER"))
	if v == "" {
		return defaultEvictAfter
	}
	if v == "off" {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return defaultEvictAfter
	}
	return d
}

// unreachableFor reports how long the node's Ready condition has been
// Unknown: the kubelet stopped reporting, as opposed to Ready=False where
// the kubelet is alive and saying why. Only Unknown is a dead or
// partitioned node.
func unreachableFor(n *corev1.Node, now time.Time) (time.Duration, bool) {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			if c.Status != corev1.ConditionUnknown || c.LastTransitionTime.IsZero() {
				return 0, false
			}
			return now.Sub(c.LastTransitionTime.Time), true
		}
	}
	return 0, false
}

// shouldEvict decides whether a node's stateless kuso pods get deleted
// now. A planned host-patch reboot is left alone: pkgupdates drains and
// reschedules around it itself.
func shouldEvict(n *corev1.Node, now time.Time, after time.Duration) bool {
	if after <= 0 || n.Annotations[pkgupdates.CordonAnnotation] == "true" {
		return false
	}
	down, ok := unreachableFor(n, now)
	return ok && down >= after
}

// evictablePod: a kuso service pod (kusoenvironment chart) run by a
// ReplicaSet, not already terminating, with no PersistentVolumeClaim.
// Deleting it on an unreachable node only sets deletionTimestamp; the
// ReplicaSet then starts a replacement elsewhere, which is what taint
// eviction would do at 300s. Volume-backed pods are skipped: a node-bound
// RWO volume can't attach anywhere else, so a replacement could only hang.
//
// The platform proxies in front of those services (kuso-activator, and
// traefik in its install namespace) qualify too: with both replicas on
// the dead node, every sleep-routed host was down for the full 300s
// taint eviction even after the apps themselves had moved.
func evictablePod(p *corev1.Pod) bool {
	if p.DeletionTimestamp != nil || !evictableApp(p) {
		return false
	}
	if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
		return false
	}
	owned := false
	for _, o := range p.OwnerReferences {
		if o.Kind == "ReplicaSet" && o.Controller != nil && *o.Controller {
			owned = true
		}
	}
	if !owned {
		return false
	}
	for _, v := range p.Spec.Volumes {
		if v.PersistentVolumeClaim != nil {
			return false
		}
	}
	return true
}

// activatorNamespace is where deploy/kuso-activator.yaml installs the
// activator.
const activatorNamespace = "kuso"

// evictableApp matches by namespace as well as name label so a tenant
// workload labelled "traefik" or "kuso-activator" isn't swept up.
func evictableApp(p *corev1.Pod) bool {
	switch p.Labels["app.kubernetes.io/name"] {
	case "kusoenvironment":
		return true
	case "kuso-activator":
		return p.Namespace == activatorNamespace
	case "traefik":
		// hack/install.sh installs traefik into "traefik"; k3s' bundled
		// one lives in kube-system.
		return p.Namespace == "traefik" || p.Namespace == "kube-system"
	}
	return false
}

func (w *Watcher) podsOnNode(ctx context.Context, nodeName string) ([]corev1.Pod, error) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	pods, err := w.Kube.Clientset.CoreV1().Pods("").List(cctx, metav1.ListOptions{
		FieldSelector: "spec.nodeName=" + nodeName,
	})
	if err != nil {
		return nil, err
	}
	return pods.Items, nil
}

// evictStateless deletes the evictable pods on an unreachable node and
// returns how many it deleted. Idempotent: already-terminating pods are
// skipped, so re-running every tick costs one LIST.
func (w *Watcher) evictStateless(ctx context.Context, nodeName string) int {
	pods, err := w.podsOnNode(ctx, nodeName)
	if err != nil {
		w.Logger.Warn("nodewatch: list pods for eviction", "node", nodeName, "err", err)
		return 0
	}
	n := 0
	for i := range pods {
		p := &pods[i]
		if p.Spec.NodeName != nodeName || !evictablePod(p) {
			continue
		}
		err := w.Kube.Clientset.CoreV1().Pods(p.Namespace).Delete(ctx, p.Name, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			w.Logger.Warn("nodewatch: evict pod from unreachable node", "node", nodeName, "pod", p.Namespace+"/"+p.Name, "err", err)
			continue
		}
		n++
	}
	if n > 0 {
		w.Logger.Info("nodewatch: evicted stateless service pods from unreachable node", "node", nodeName, "pods", n)
	}
	return n
}

// addonsOnNode names the addon pods (namespace/addon) on a node. A
// StatefulSet won't replace a pod it can't confirm is gone, and a
// local-path volume is bound to that node anyway, so these stay down
// until the node returns or someone deletes the node.
func addonsOnNode(pods []corev1.Pod) []string {
	seen := map[string]bool{}
	var out []string
	for i := range pods {
		p := &pods[i]
		name := p.Labels["kuso.sislelabs.com/addon"]
		if name == "" || p.Labels["app.kubernetes.io/name"] != "kusoaddon" {
			continue
		}
		key := p.Namespace + "/" + name
		if !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// addonGuidance is appended to the node.unreachable card when addons
// are stranded on the node.
func addonGuidance(addons []string) string {
	if len(addons) == 0 {
		return ""
	}
	return "\n\nAddons on this node stay down until it returns (their data lives on its disk): " +
		strings.Join(addons, ", ") +
		". If the node is gone for good, delete it from Settings → Nodes and restore those addons from backup."
}
