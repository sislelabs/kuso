package health

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
	"kuso/server/internal/notify"
)

// Node disk fill, read from each kubelet's summary rather than from the
// NodeMetric rows in the control-plane Postgres: the disk that fills is
// often the one that Postgres (or its WAL) lives on. DiskPressure fires
// only once the kubelet is already evicting; this fires at DiskWarnPct.
// Local-path volumes (platform PVCs, Prometheus' emptyDir, addon data)
// grow into the node disk, so this is also where their growth shows up.

// nodeDiskConfirmChecks: a node must read past the threshold on this
// many consecutive checks before it pages, so the first check after a
// restart or upgrade never pages on sight.
const nodeDiskConfirmChecks = 2

func (w *Watcher) readNodeDisks(ctx context.Context, nodes []*corev1.Node) map[string]kube.NodeFS {
	if w.nodeDisks != nil {
		return w.nodeDisks(ctx, nodes)
	}
	out, _ := w.Kube.NodeFilesystems(ctx, nodes)
	return out
}

// checkNodeDisks runs on the platform-volume cadence. onDisk lists, per
// node, the not-size-limited platform volumes found on it, so the alert
// can name them without blaming any one of them.
func (w *Watcher) checkNodeDisks(ctx context.Context, onDisk map[string][]string) {
	threshold := w.DiskWarnPct
	if threshold <= 0 {
		threshold = volumeWarnPct
	}
	lctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	nodes, err := w.Kube.Clientset.CoreV1().Nodes().List(lctx, metav1.ListOptions{})
	if err != nil {
		return
	}
	ptrs := make([]*corev1.Node, 0, len(nodes.Items))
	for i := range nodes.Items {
		ptrs = append(ptrs, &nodes.Items[i])
	}
	disks := w.readNodeDisks(lctx, ptrs)
	for _, n := range ptrs {
		fs, ok := disks[n.Name]
		if !ok || fs.CapacityBytes <= 0 {
			continue
		}
		used := fs.CapacityBytes - fs.AvailableBytes
		pct := int(used * 100 / fs.CapacityBytes)
		key := "node:" + n.Name + ":disk-fill"
		w.mu.Lock()
		if w.diskOver == nil {
			w.diskOver = map[string]int{}
		}
		fire := false
		switch {
		case pct >= threshold:
			w.diskOver[key]++
			fire = !w.fired[key] && w.diskOver[key] >= nodeDiskConfirmChecks
			if fire {
				w.fired[key] = true
			}
		case pct < volumeClearPct:
			delete(w.diskOver, key)
			delete(w.fired, key)
		default:
			delete(w.diskOver, key)
		}
		w.mu.Unlock()
		if !fire {
			continue
		}
		body := fmt.Sprintf("%s of %s used on %s. Kubelet starts evicting pods at its hard threshold, and Postgres stops accepting writes when its disk is full.",
			gib(used), gib(fs.CapacityBytes), n.Name)
		if vols := onDisk[n.Name]; len(vols) > 0 {
			sort.Strings(vols)
			body += " Not size-limited volumes on this disk: " + strings.Join(vols, ", ") +
				". The reading is the whole disk, so check what is actually growing (container images, logs, volumes) before pruning one of them."
		}
		err := w.sendDurable(notify.AlertFired(
			fmt.Sprintf("Node %s disk %d%% full", n.Name, pct),
			body,
			"warn",
			map[string]string{"node": n.Name},
		))
		if err != nil {
			// Not delivered (the outbox is in Postgres, maybe on this very
			// disk): retry on the next check rather than stay silent.
			w.mu.Lock()
			delete(w.fired, key)
			w.mu.Unlock()
		}
	}
}
