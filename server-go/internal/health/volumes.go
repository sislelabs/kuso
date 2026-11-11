package health

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
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
	w.checkDataVolumes(ctx)
	for _, v := range platformVolumes {
		usage, request, ok := w.platformVolumeUsage(ctx, v)
		if !ok || request <= 0 {
			continue
		}
		pct, what := volumeFill(usage, request)
		if pct < 0 {
			continue
		}
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
			title := fmt.Sprintf("%s storage %d%% full", v.Label, pct)
			body := fmt.Sprintf("%s/%s uses %s of its %s. %s", w.Namespace, v.PVC, gib(usage.UsedBytes), gib(request), v.Hint)
			if what == fillNodeDisk {
				title = fmt.Sprintf("%s node disk %d%% full", v.Label, pct)
				body = fmt.Sprintf("%s/%s is not size-limited (local-path), so it grows into its node's disk, which is %s of %s used. %s",
					w.Namespace, v.PVC, gib(usage.UsedBytes), gib(usage.CapacityBytes), v.Hint)
			}
			w.send(notify.AlertFired(
				title,
				body,
				"warn",
				map[string]string{"pvc": v.PVC},
			))
		}
	}
}

type fillKind int

const (
	fillVolume fillKind = iota
	fillNodeDisk
)

// volumeFill turns a kubelet reading into a fill percentage. When the
// reported capacity is far above the request the volume isn't
// size-enforced (local-path): UsedBytes is the node disk's usage, so
// compare it to the disk, not the request; dividing by the request
// reported a registry on a 62%-full disk as "882% full". -1 = no reading.
func volumeFill(u kube.PVCUsage, request int64) (int, fillKind) {
	if u.CapacityBytes > 0 && u.CapacityBytes > request*2 {
		return int(u.UsedBytes * 100 / u.CapacityBytes), fillNodeDisk
	}
	if request <= 0 {
		return -1, fillVolume
	}
	return int(u.UsedBytes * 100 / request), fillVolume
}

// platformVolumeUsage returns the PVC's usage (from the kubelet of
// the node running its pod) and its requested size. ok=false on any
// read error: a missing grant (nodes/stats) or a pod mid-reschedule.
func (w *Watcher) platformVolumeUsage(ctx context.Context, v platformVolume) (usage kube.PVCUsage, request int64, ok bool) {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	pvc, err := w.Kube.Clientset.CoreV1().PersistentVolumeClaims(w.Namespace).Get(cctx, v.PVC, metav1.GetOptions{})
	if err != nil {
		return kube.PVCUsage{}, 0, false
	}
	q := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	request = q.Value()
	pods, err := w.Kube.Clientset.CoreV1().Pods(w.Namespace).List(cctx, metav1.ListOptions{LabelSelector: v.Selector})
	if err != nil {
		return kube.PVCUsage{}, 0, false
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Spec.NodeName == "" || p.Status.Phase != corev1.PodRunning {
			continue
		}
		node, err := w.Kube.Clientset.CoreV1().Nodes().Get(cctx, p.Spec.NodeName, metav1.GetOptions{})
		if err != nil {
			return kube.PVCUsage{}, 0, false
		}
		byPVC, err := w.Kube.NodePVCUsage(cctx, node)
		if err != nil {
			return kube.PVCUsage{}, 0, false
		}
		u, found := byPVC[w.Namespace+"/"+v.PVC]
		return u, request, found
	}
	return kube.PVCUsage{}, 0, false
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

// Database volumes (addon StatefulSets, the shared instance Postgres, the
// control-plane CNPG cluster) on a size-enforced CSI volume reach 100% with
// no other signal: the DB stops accepting writes, and on the instance PG
// that is every tenant at once. Local-path volumes grow into the node disk,
// which the node-disk alerts already cover, so they are skipped here.
const (
	dataVolumeWarnPct  = 75
	dataVolumeCritPct  = 85
	dataVolumeClearPct = 70
	// controlPlaneCluster is the CNPG cluster holding kuso's own database.
	controlPlaneCluster = "kuso-postgres"
)

// dataVolume is one database PVC mounted by a running pod.
type dataVolume struct {
	Namespace, PVC, Node, Owner string
}

// dataVolumeTargets picks the PVCs mounted by running addon pods (backup and
// restore Job pods carry the addon label too, but also a role label, and
// hold only scratch space) and the control-plane Postgres pods.
func dataVolumeTargets(pods []corev1.Pod) []dataVolume {
	var out []dataVolume
	seen := map[string]bool{}
	for i := range pods {
		p := &pods[i]
		if p.Spec.NodeName == "" || p.Status.Phase != corev1.PodRunning {
			continue
		}
		owner := p.Labels["kuso.sislelabs.com/addon"]
		if owner == "" && p.Labels["cnpg.io/cluster"] == controlPlaneCluster {
			owner = "control-plane database"
		}
		if owner == "" || p.Labels["kuso.sislelabs.com/role"] != "" {
			continue
		}
		for _, v := range p.Spec.Volumes {
			if v.PersistentVolumeClaim == nil {
				continue
			}
			k := p.Namespace + "/" + v.PersistentVolumeClaim.ClaimName
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, dataVolume{Namespace: p.Namespace, PVC: v.PersistentVolumeClaim.ClaimName, Node: p.Spec.NodeName, Owner: owner})
		}
	}
	return out
}

// dataVolumeAlert is the two-level transition for one reading: warn at
// dataVolumeWarnPct, critical at dataVolumeCritPct (each fires once), both
// re-arm below dataVolumeClearPct. sev is "" when nothing fires.
func dataVolumeAlert(pct int, warned, critical bool) (sev string, nowWarned, nowCritical bool) {
	switch {
	case pct < dataVolumeClearPct:
		return "", false, false
	case pct >= dataVolumeCritPct && !critical:
		return "error", true, true
	case pct >= dataVolumeWarnPct && !warned:
		return "warn", true, critical
	}
	return "", warned, critical
}

// checkDataVolumes uses only reads the kuso-server ClusterRole already
// grants (pods list, persistentvolumeclaims get, nodes get, nodes/stats);
// any failed read skips that volume for this pass.
func (w *Watcher) checkDataVolumes(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cs := w.Kube.Clientset.CoreV1()
	var pods []corev1.Pod
	for _, q := range []struct{ ns, sel string }{
		{metav1.NamespaceAll, "kuso.sislelabs.com/addon,!kuso.sislelabs.com/role"},
		{w.Namespace, "cnpg.io/cluster=" + controlPlaneCluster},
	} {
		l, err := cs.Pods(q.ns).List(cctx, metav1.ListOptions{LabelSelector: q.sel})
		if err != nil {
			continue
		}
		pods = append(pods, l.Items...)
	}
	summaries := map[string]map[string]kube.PVCUsage{}
	for _, v := range dataVolumeTargets(pods) {
		byPVC, done := summaries[v.Node]
		if !done {
			if node, err := cs.Nodes().Get(cctx, v.Node, metav1.GetOptions{}); err == nil {
				byPVC, _ = w.Kube.NodePVCUsage(cctx, node)
			}
			summaries[v.Node] = byPVC
		}
		usage, ok := byPVC[v.Namespace+"/"+v.PVC]
		if !ok {
			continue
		}
		pvc, err := cs.PersistentVolumeClaims(v.Namespace).Get(cctx, v.PVC, metav1.GetOptions{})
		if err != nil {
			continue
		}
		q := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
		pct, what := volumeFill(usage, q.Value())
		if pct < 0 || what == fillNodeDisk {
			continue
		}
		wk, ck := "dbpvc:"+v.Namespace+"/"+v.PVC+":warn", "dbpvc:"+v.Namespace+"/"+v.PVC+":crit"
		w.mu.Lock()
		sev, warned, critical := dataVolumeAlert(pct, w.fired[wk], w.fired[ck])
		setFired(w.fired, wk, warned)
		setFired(w.fired, ck, critical)
		w.mu.Unlock()
		if sev == "" {
			continue
		}
		w.send(notify.AlertFired(
			fmt.Sprintf("Database volume %d%% full", pct),
			fmt.Sprintf("%s/%s (%s) uses %s of its %s. A full volume stops the database accepting writes; grow the addon's storageSize or free space.",
				v.Namespace, v.PVC, v.Owner, gib(usage.UsedBytes), gib(q.Value())),
			sev,
			map[string]string{"pvc": v.PVC, "namespace": v.Namespace},
		))
	}
}

func setFired(m map[string]bool, k string, on bool) {
	if on {
		m[k] = true
	} else {
		delete(m, k)
	}
}
