package health

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/notify"
)

// platformVolume is a platform PVC whose filling up breaks every build
// (SCL-4: the registry's blob store, which only shrinks at the GC run;
// buildkitd's cache). On local-path the PVC size isn't enforced, so a
// full registry fills the node disk instead of failing cleanly.
type platformVolume struct {
	PVC      string
	Selector string
	Label    string
	Hint     string
}

var platformVolumes = []platformVolume{
	{
		PVC: "kuso-registry-data", Selector: "app.kubernetes.io/name=kuso-registry", Label: "Image registry",
		Hint: "Pushes start failing once it is full. Lower KUSO_BUILD_IMAGE_RETENTION on kuso-server (default 5 images per service), run the kuso-registry-gc CronJob now (kubectl -n kuso create job --from=cronjob/kuso-registry-gc gc-now), or grow the PVC.",
	},
	{
		PVC: "kuso-buildkitd-state", Selector: "app.kubernetes.io/name=kuso-buildkitd", Label: "Build cache",
		Hint: "buildkitd garbage-collects to maxUsedSpace in kuso-buildkitd-config; if that is above the PVC size, lower it.",
	},
}

const (
	volumeWarnPct  = 85
	volumeClearPct = 75
	// volumeCheckEvery: ticks between checks (the watcher ticks every
	// minute; a kubelet summary per check is cheap but not free).
	volumeCheckEvery = 10
)

// volumeAlertState decides the transition for one volume reading:
// fire once at volumeWarnPct, clear below volumeClearPct.
func volumeAlertState(usedPct int, fired bool) (fire, clear bool) {
	if !fired && usedPct >= volumeWarnPct {
		return true, false
	}
	if fired && usedPct < volumeClearPct {
		return false, true
	}
	return false, false
}

func (w *Watcher) checkPlatformVolumes(ctx context.Context) {
	w.mu.Lock()
	w.volumeTick++
	due := w.volumeTick%volumeCheckEvery == 1
	w.mu.Unlock()
	if !due || w.Kube == nil || w.Kube.Clientset == nil {
		return
	}
	for _, v := range platformVolumes {
		used, request, ok := w.platformVolumeUsage(ctx, v)
		if !ok || request <= 0 {
			continue
		}
		pct := int(used * 100 / request)
		key := "pvc:" + v.PVC + ":full"
		w.mu.Lock()
		fire, clear := volumeAlertState(pct, w.fired[key])
		if fire {
			w.fired[key] = true
		}
		if clear {
			delete(w.fired, key)
		}
		w.mu.Unlock()
		if fire {
			w.send(notify.AlertFired(
				fmt.Sprintf("%s storage %d%% full", v.Label, pct),
				fmt.Sprintf("%s/%s uses %s of its %s. %s", w.Namespace, v.PVC, gib(used), gib(request), v.Hint),
				"warn",
				map[string]string{"pvc": v.PVC},
			))
		}
	}
}

// platformVolumeUsage returns the PVC's used bytes (from the kubelet of
// the node running its pod) and its requested size. ok=false on any
// read error: a missing grant (nodes/stats) or a pod mid-reschedule.
func (w *Watcher) platformVolumeUsage(ctx context.Context, v platformVolume) (used, request int64, ok bool) {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	pvc, err := w.Kube.Clientset.CoreV1().PersistentVolumeClaims(w.Namespace).Get(cctx, v.PVC, metav1.GetOptions{})
	if err != nil {
		return 0, 0, false
	}
	q := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	request = q.Value()
	pods, err := w.Kube.Clientset.CoreV1().Pods(w.Namespace).List(cctx, metav1.ListOptions{LabelSelector: v.Selector})
	if err != nil {
		return 0, 0, false
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Spec.NodeName == "" || p.Status.Phase != corev1.PodRunning {
			continue
		}
		node, err := w.Kube.Clientset.CoreV1().Nodes().Get(cctx, p.Spec.NodeName, metav1.GetOptions{})
		if err != nil {
			return 0, 0, false
		}
		usage, err := w.Kube.NodePVCUsage(cctx, node)
		if err != nil {
			return 0, 0, false
		}
		u, found := usage[w.Namespace+"/"+v.PVC]
		return u, request, found
	}
	return 0, 0, false
}

const (
	// backlogWarnAfter: a build queued this long means the build cap
	// (daemons × max-parallelism × 2) is the bottleneck, not a burst.
	backlogWarnAfter  = 20 * time.Minute
	backlogClearAfter = 5 * time.Minute
	backlogKey        = "builds:backlog"
)

// checkBuildBacklog alerts once when the oldest queued build has waited
// past backlogWarnAfter, and re-arms when the queue drains (SCL-3).
func (w *Watcher) checkBuildBacklog(ctx context.Context) {
	if w.BuildBacklog == nil {
		return
	}
	n, oldest := w.BuildBacklog(ctx)
	wait := time.Duration(0)
	if n > 0 && !oldest.IsZero() {
		wait = w.clock().Sub(oldest)
	}
	w.mu.Lock()
	fired := w.fired[backlogKey]
	fire := !fired && wait >= backlogWarnAfter
	if fire {
		w.fired[backlogKey] = true
	}
	if fired && wait < backlogClearAfter {
		delete(w.fired, backlogKey)
	}
	w.mu.Unlock()
	if fire {
		w.send(notify.AlertFired(
			fmt.Sprintf("Build queue backlog: %d builds waiting", n),
			fmt.Sprintf("The oldest queued build has waited %s. Builds are capped by buildkitd capacity "+
				"(daemons × max-parallelism × 2). Add daemons with deploy/optional/buildkitd-shards.yaml "+
				"and KUSO_BUILDKITD_SHARDS, or raise KUSO_BUILD_MAX_CONCURRENT if the daemon has headroom.",
				wait.Round(time.Minute)),
			"warn",
			map[string]string{"queued": fmt.Sprint(n)},
		))
	}
}

func gib(b int64) string {
	return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30))
}
