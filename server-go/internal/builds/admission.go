// Admission control + namespace resolution for the build pipeline.
// Extracted from builds.go in the v0.12 refactor pass to keep the
// concurrency-cap / pod-counting logic separate from the lifecycle
// (Create/Cancel/Rollback) and notification-card surfaces. No
// behaviour change vs the pre-split shape; tests still drive the
// public Service methods.
package builds

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/kube"
)

// admitBuild enforces the concurrent-build cap. Returns a release
// function the caller MUST call when its build is done (even if
// admission failed — release is no-op then). capHit=true tells the
// caller the build was queued, not started.
func (s *Service) admitBuild(ctx context.Context, project string) (release func(), capHit bool, err error) {
	cfg := s.loadSettings(ctx)
	if cfg.MaxConcurrent <= 0 {
		return func() {}, false, nil
	}
	// Per-project lower bound. Cheap CR read; only matters when set.
	projectCap := s.projectBuildCap(ctx, project)
	if projectCap > 0 {
		if active := s.countActiveBuildsForProject(ctx, project); active >= projectCap {
			return func() {}, true, nil
		}
	}
	// Cluster-wide cap based on reality. Counts running build pods
	// across every namespace, which catches builds rendered by the
	// operator from queued CRs, builds left over from a previous
	// kuso-server replica, and builds re-spawned by a Job retry.
	if active := s.countRunningBuildPodsCluster(ctx); active >= cfg.MaxConcurrent {
		return func() {}, true, nil
	}
	return func() {}, false, nil
}

// countRunningBuildPodsCluster lists pods labelled as kusobuild
// across all namespaces and returns the count whose phase is Pending
// or Running. Best-effort: kube errors return 0 (admit) — we'd rather
// risk one extra build than wedge the system on a transient apiserver
// hiccup.
func (s *Service) countRunningBuildPodsCluster(ctx context.Context) int {
	if s.Kube == nil {
		return 0
	}
	lctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	// First: build a set of build-CR names whose state is "done"
	// (succeeded / failed / cancelled). Pods owned by these CRs
	// shouldn't count toward the active cap — they're orphans the
	// operator failed to clean up (we've seen this happen after
	// operator restarts, where the initial-cache-sync ignores the
	// state=done watch selector and re-renders cancelled builds).
	// Without filtering, a single stuck cancelled-build Job pegged
	// the cluster cap at 1 and wedged every Redeploy.
	doneNames := map[string]struct{}{}
	doneSel, _ := labels.Parse("kuso.sislelabs.com/build-state=done")
	if blist, ok := s.Kube.Cache.ListFromCache(kube.GVRBuilds, "", doneSel); ok {
		for _, u := range blist {
			doneNames[u.GetName()] = struct{}{}
		}
	} else if rawBlist, berr := s.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace("").List(lctx, metav1.ListOptions{
		LabelSelector: "kuso.sislelabs.com/build-state=done",
	}); berr == nil {
		for i := range rawBlist.Items {
			doneNames[rawBlist.Items[i].GetName()] = struct{}{}
		}
	}
	seen := map[string]struct{}{}
	const selStr = "app.kubernetes.io/component=kusobuild"
	sel, err := labels.Parse(selStr)
	if err != nil {
		return 0
	}
	pods, ok := s.Kube.Cache.ListPodsByLabel(sel)
	if !ok {
		rawPods, lerr := s.Kube.Clientset.CoreV1().Pods("").List(lctx, metav1.ListOptions{
			LabelSelector: selStr,
		})
		if lerr != nil {
			slog.Default().Warn("countRunningBuildPodsCluster", "selector", selStr, "err", lerr)
			return 0
		}
		for i := range rawPods.Items {
			accept(seen, doneNames, &rawPods.Items[i])
		}
		return len(seen)
	}
	for _, p := range pods {
		accept(seen, doneNames, p)
	}
	return len(seen)
}

// accept records a pod into seen iff it's pending/running and not
// owned by a build CR in the doneNames orphan set. Shared between
// the cluster and per-project counters.
func accept(seen, doneNames map[string]struct{}, p *corev1.Pod) {
	if p.Status.Phase != corev1.PodPending && p.Status.Phase != corev1.PodRunning {
		return
	}
	if _, isDone := doneNames[p.Labels["app.kubernetes.io/instance"]]; isDone {
		return
	}
	seen[p.Namespace+"/"+p.Name] = struct{}{}
}

// projectBuildCap returns the per-project max-concurrent override
// from the KusoProject CR's annotation, or 0 when unset / unparseable.
// Best-effort: any kube error returns 0 (use the global cap).
func (s *Service) projectBuildCap(ctx context.Context, project string) int {
	if s.Kube == nil || project == "" {
		return 0
	}
	gctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	p, err := s.Kube.GetKusoProject(gctx, s.Namespace, project)
	if err != nil || p == nil {
		return 0
	}
	v := p.Annotations["kuso.sislelabs.com/build-max-concurrent"]
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// countActiveBuildsForProject returns the number of currently-running
// build pods for a project (not CRs — queued CRs don't render pods
// and don't consume resources). Best-effort: kube errors return 0
// (admit) — we'd rather risk one extra build than wedge.
func (s *Service) countActiveBuildsForProject(ctx context.Context, project string) int {
	if s.Kube == nil || project == "" {
		return 0
	}
	lctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ns := s.nsFor(lctx, project)
	// Same orphan-pod filter as countRunningBuildPodsCluster: skip
	// pods owned by build CRs labelled state=done. See that function
	// for the why.
	doneNames := map[string]struct{}{}
	doneSelStr := kube.LabelSelector(map[string]string{
		kube.LabelProject:                project,
		"kuso.sislelabs.com/build-state": "done",
	})
	if doneSel, perr := labels.Parse(doneSelStr); perr == nil {
		if blist, ok := s.Kube.Cache.ListFromCache(kube.GVRBuilds, ns, doneSel); ok {
			for _, u := range blist {
				doneNames[u.GetName()] = struct{}{}
			}
		} else if rawBlist, berr := s.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).List(lctx, metav1.ListOptions{
			LabelSelector: doneSelStr,
		}); berr == nil {
			for i := range rawBlist.Items {
				doneNames[rawBlist.Items[i].GetName()] = struct{}{}
			}
		}
	}
	seen := map[string]struct{}{}
	selStr := kube.LabelSelector(map[string]string{
		"app.kubernetes.io/component": "kusobuild",
		kube.LabelProject:             project,
	})
	sel, err := labels.Parse(selStr)
	if err != nil {
		return 0
	}
	pods, ok := s.Kube.Cache.ListPodsByLabel(sel)
	if !ok {
		rawPods, lerr := s.Kube.Clientset.CoreV1().Pods(ns).List(lctx, metav1.ListOptions{LabelSelector: selStr})
		if lerr != nil {
			return 0
		}
		for i := range rawPods.Items {
			accept(seen, doneNames, &rawPods.Items[i])
		}
		return len(seen)
	}
	for _, p := range pods {
		if p.Namespace != ns {
			continue
		}
		accept(seen, doneNames, p)
	}
	return len(seen)
}

// findRecentForBranch returns the newest in-flight (running / pending
// / queued) KusoBuild for (project, fqn, branch) created within
// `window`, or nil if none. Used to coalesce rapid synthetic-ref
// redeploys so spam-clicking the Redeploy button doesn't pile up
// duplicate queue entries.
func (s *Service) findRecentForBranch(ctx context.Context, ns, project, fqn, branch string, window time.Duration) (*kube.KusoBuild, error) {
	if s.Kube == nil {
		return nil, nil
	}
	lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := s.Kube.ListKusoBuildsByLabels(lctx, ns, map[string]string{
		kube.LabelProject: project,
		kube.LabelService: fqn,
	})
	if err != nil {
		return nil, fmt.Errorf("list recent builds: %w", err)
	}
	cutoff := time.Now().Add(-window)
	var best *kube.KusoBuild
	for i := range raw {
		b := raw[i]
		if b.Labels["kuso.sislelabs.com/build-state"] == "done" {
			continue
		}
		if b.Spec.Branch != branch {
			continue
		}
		if !b.CreationTimestamp.Time.IsZero() && b.CreationTimestamp.Time.Before(cutoff) {
			continue
		}
		if best == nil || b.CreationTimestamp.After(best.CreationTimestamp.Time) {
			b := b
			best = &b
		}
	}
	return best, nil
}

// findActiveForService returns the name of an in-flight KusoBuild for
// (project, fqn), or "" if none. "In-flight" = no `build-state` label
// yet (running/pending/queued).
func (s *Service) findActiveForService(ctx context.Context, ns, project, fqn string) (string, error) {
	if s.Kube == nil {
		return "", nil
	}
	lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := s.Kube.ListKusoBuildsByLabels(lctx, ns, map[string]string{
		kube.LabelProject: project,
		kube.LabelService: fqn,
	})
	if err != nil {
		return "", fmt.Errorf("list active builds: %w", err)
	}
	for i := range raw {
		if raw[i].Labels["kuso.sislelabs.com/build-state"] == "" {
			return raw[i].Name, nil
		}
	}
	return "", nil
}

// awaitPodGone polls Pods.List for build pods owned by `buildName`
// until none remain or `timeout` elapses. Best-effort; on timeout we
// proceed without an error since the kubelet will eventually reap.
// The Cancel HTTP path uses this so a UI refetch after cancel sees a
// clean state instead of a "still running" pod row that the kubelet
// deletes 30 seconds later.
func awaitPodGone(ctx context.Context, kc *kube.Client, ns, buildName string, timeout time.Duration) {
	if kc == nil {
		return
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		pods, err := kc.Clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
			LabelSelector: kube.LabelSelector(map[string]string{"app.kubernetes.io/instance": buildName}),
		})
		if err != nil {
			return
		}
		alive := 0
		for i := range pods.Items {
			ph := pods.Items[i].Status.Phase
			if ph == corev1.PodPending || ph == corev1.PodRunning {
				alive++
			}
		}
		if alive == 0 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// supersedePriorBuilds is retained for the cleanup path — it's no
// longer called from Create (v0.8.5: same-service builds queue rather
// than supersede). Other callers may still want the bulk-cancel
// semantics so we leave the helper in place.
//
// Finds any in-flight KusoBuild for (project, fqn) other than
// newName, stamps it as cancelled, and tears down its kaniko Job.
// Best-effort: kube errors are logged at warn and swallowed.
func (s *Service) supersedePriorBuilds(ctx context.Context, ns, project, fqn, newName string) {
	if s.Kube == nil {
		return
	}
	lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := s.Kube.ListKusoBuildsByLabels(lctx, ns, map[string]string{
		kube.LabelProject: project,
		kube.LabelService: fqn,
	})
	if err != nil {
		slog.Default().Warn("builds: list active for supersede", "err", err, "project", project, "service", fqn)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for i := range raw {
		if raw[i].Labels["kuso.sislelabs.com/build-state"] != "" {
			continue
		}
		name := raw[i].Name
		if name == newName {
			continue
		}
		patch := fmt.Sprintf(
			`{"metadata":{"annotations":{%q:"cancelled",%q:%q,%q:%q,%q:%q},"labels":{"kuso.sislelabs.com/build-state":"done"}},"spec":{"done":true}}`,
			annPhase,
			annCompletedAt, now,
			annSupersededBy, newName,
			annMessage, "superseded by "+newName,
		)
		if _, perr := s.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).
			Patch(lctx, name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); perr != nil {
			slog.Default().Warn("builds: patch superseded", "err", perr, "build", name)
			continue
		}
		bg := metav1.DeletePropagationBackground
		if jerr := s.Kube.Clientset.BatchV1().Jobs(ns).Delete(lctx, name, metav1.DeleteOptions{
			PropagationPolicy: &bg,
		}); jerr != nil && !apierrors.IsNotFound(jerr) {
			slog.Default().Warn("builds: delete superseded job", "err", jerr, "build", name)
		}
		if s.Notifier != nil {
			short := strings.TrimPrefix(fqn, project+"-")
			title, desc, fields := buildRichCard(&raw[i], short, "superseded", "", "")
			if desc == "" {
				desc = "Replaced by `" + newName + "`"
			}
			s.Notifier.Emit(EventEnvelope{
				Type:        eventBuildSuperseded,
				Title:       title,
				Description: desc,
				Project:     project,
				Service:     short,
				URL:         buildEventURL(project, short),
				Severity:    "info",
				DurationMs:  buildDurationMs(&raw[i]),
				Fields:      fields,
			})
		}
	}
}

// nsFor returns the execution namespace for project, defaulting to
// the home Namespace.
func (s *Service) nsFor(ctx context.Context, project string) string {
	if s.NSResolver == nil || project == "" {
		return s.Namespace
	}
	return s.NSResolver.NamespaceFor(ctx, project)
}

// ScanNamespaces returns every namespace the build poller / promotion
// flow needs to walk: the home ns plus every distinct spec.namespace
// declared by a KusoProject. Deduped, errors swallowed (always at
// least the home ns is returned).
func (s *Service) ScanNamespaces(ctx context.Context) []string {
	out := []string{s.Namespace}
	seen := map[string]bool{s.Namespace: true}
	if s.Kube == nil {
		return out
	}
	projects, err := s.Kube.ListKusoProjects(ctx, s.Namespace)
	if err != nil {
		return out
	}
	for _, p := range projects {
		ns := p.Spec.Namespace
		if ns == "" || seen[ns] {
			continue
		}
		seen[ns] = true
		out = append(out, ns)
	}
	return out
}

