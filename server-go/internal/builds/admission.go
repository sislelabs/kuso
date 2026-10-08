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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/kube"
)

// admitSlot is one build counted against the caps before the informer can
// see it: a reservation (Create admitted, CR not written yet) or a recent
// start (CR written, maybe not in the cache yet).
type admitSlot struct {
	project string
	at      time.Time
}

// admitRecentTTL bounds how long a just-started build is counted from
// memory. The informer sees a new CR within seconds; after that the CR
// itself is counted.
const admitRecentTTL = time.Minute

// admitBuild enforces the concurrent-build cap. Returns a release
// function the caller MUST call when it is done creating (even if
// admission failed — release is a no-op then). capHit=true tells the
// caller the build was queued, not started.
//
// An admitted Create holds a reservation until release, and
// noteBuildStarted turns it into a recent start once the CR exists. Both
// count against the caps, so a monorepo push that calls Create for 12
// services back to back admits only MaxConcurrent of them. Counting only
// build pods admitted all 12: a CR has no pod for the 1-3s it takes the
// controller to render its Job.
func (s *Service) admitBuild(ctx context.Context, project string) (release func(), capHit bool, err error) {
	cfg := s.loadSettings(ctx)
	if cfg.MaxConcurrent <= 0 {
		return func() {}, false, nil
	}
	projectCap := s.projectBuildCap(ctx, project)

	s.admitMu.Lock()
	defer s.admitMu.Unlock()
	if projectCap > 0 && s.countActiveBuildsForProjectLocked(ctx, project) >= projectCap {
		return func() {}, true, nil
	}
	if s.countRunningBuildPodsClusterLocked(ctx) >= cfg.MaxConcurrent {
		return func() {}, true, nil
	}
	slot := &admitSlot{project: project, at: time.Now()}
	if s.admitReserved == nil {
		s.admitReserved = map[*admitSlot]struct{}{}
	}
	s.admitReserved[slot] = struct{}{}
	return func() {
		s.admitMu.Lock()
		delete(s.admitReserved, slot)
		s.admitMu.Unlock()
	}, false, nil
}

// noteBuildStarted records a build this process just started (a non-queued
// Create, or a queued build the dispatcher promoted) so the caps count it
// until the informer has caught up.
func (s *Service) noteBuildStarted(ns, name, project string) {
	s.admitMu.Lock()
	defer s.admitMu.Unlock()
	if s.admitRecent == nil {
		s.admitRecent = map[string]admitSlot{}
	}
	s.admitRecent[ns+"/"+name] = admitSlot{project: project, at: time.Now()}
}

// countRunningBuildPodsCluster returns the number of builds currently
// occupying a cluster-wide build slot. See countActiveBuilds.
func (s *Service) countRunningBuildPodsCluster(ctx context.Context) int {
	s.admitMu.Lock()
	defer s.admitMu.Unlock()
	return s.countRunningBuildPodsClusterLocked(ctx)
}

func (s *Service) countRunningBuildPodsClusterLocked(ctx context.Context) int {
	return s.countActiveBuilds(ctx, "", "")
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

// countActiveBuildsForProject returns the number of builds currently
// occupying one of project's build slots. See countActiveBuilds.
func (s *Service) countActiveBuildsForProject(ctx context.Context, project string) int {
	if project == "" {
		return 0
	}
	s.admitMu.Lock()
	defer s.admitMu.Unlock()
	return s.countActiveBuildsForProjectLocked(ctx, project)
}

func (s *Service) countActiveBuildsForProjectLocked(ctx context.Context, project string) int {
	if s.Kube == nil || project == "" {
		return 0
	}
	lctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return s.countActiveBuilds(lctx, s.nsFor(lctx, project), project)
}

// countActiveBuilds counts the builds that occupy a build slot, cluster
// wide (project == "") or for one project in ns. A build is counted once
// by name when any of these hold:
//   - its CR is started and not finished: no build-state label (so neither
//     queued nor done), and not merely awaiting promotion or a release
//     hook, which use no build resources;
//   - it has a Pending or Running build pod whose CR isn't done (catches
//     Job retries and pods from a previous kuso-server);
//   - this process started it within admitRecentTTL.
//
// Admission reservations are added on top. Best-effort: kube errors count
// as zero (admit) — we'd rather risk one extra build than wedge the
// system on a transient apiserver hiccup. Caller holds admitMu.
func (s *Service) countActiveBuilds(ctx context.Context, ns, project string) int {
	if s.Kube == nil {
		return 0
	}
	lctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	active := map[string]struct{}{}
	inScope := func(objNS, objProject string) bool {
		if project == "" {
			return true
		}
		return objNS == ns && objProject == project
	}

	// Pods of done CRs are orphans the operator failed to clean up
	// (seen after operator restarts, where the initial cache sync
	// re-renders cancelled builds). Without this filter a single stuck
	// cancelled-build Job pegged the cluster cap and wedged every
	// Redeploy.
	doneNames := map[string]struct{}{}
	for _, b := range s.listBuildsForAdmission(lctx, ns, project, "kuso.sislelabs.com/build-state=done") {
		doneNames[b.GetName()] = struct{}{}
	}
	for _, b := range s.listBuildsForAdmission(lctx, ns, project, "!kuso.sislelabs.com/build-state") {
		ann := b.GetAnnotations()
		if ann[annPromoteHold] != "" || ann[annJobSucceeded] != "" {
			continue
		}
		if done, _, _ := unstructured.NestedBool(b.Object, "spec", "done"); done {
			continue
		}
		active[b.GetNamespace()+"/"+b.GetName()] = struct{}{}
	}

	podSel := map[string]string{"app.kubernetes.io/component": "kusobuild"}
	if project != "" {
		podSel[kube.LabelProject] = project
	}
	selStr := kube.LabelSelector(podSel)
	addPod := func(p *corev1.Pod) {
		if p.Status.Phase != corev1.PodPending && p.Status.Phase != corev1.PodRunning {
			return
		}
		if !inScope(p.Namespace, p.Labels[kube.LabelProject]) {
			return
		}
		inst := p.Labels["app.kubernetes.io/instance"]
		if _, isDone := doneNames[inst]; isDone {
			return
		}
		if inst == "" {
			inst = "pod:" + p.Name
		}
		active[p.Namespace+"/"+inst] = struct{}{}
	}
	if sel, err := labels.Parse(selStr); err == nil {
		if pods, ok := s.Kube.Cache.ListPodsByLabel(sel); ok {
			for _, p := range pods {
				addPod(p)
			}
		} else if raw, lerr := s.Kube.Clientset.CoreV1().Pods(ns).List(lctx, metav1.ListOptions{LabelSelector: selStr}); lerr == nil {
			for i := range raw.Items {
				addPod(&raw.Items[i])
			}
		} else {
			slog.Default().Warn("count active builds: list pods", "selector", selStr, "err", lerr)
		}
	}

	now := time.Now()
	for key, r := range s.admitRecent {
		if now.Sub(r.at) > admitRecentTTL {
			delete(s.admitRecent, key)
			continue
		}
		keyNS, _, _ := strings.Cut(key, "/")
		if inScope(keyNS, r.project) {
			active[key] = struct{}{}
		}
	}
	n := len(active)
	for r := range s.admitReserved {
		if project == "" || r.project == project {
			n++
		}
	}
	return n
}

// listBuildsForAdmission lists KusoBuild CRs matching selector (plus the
// project label when project is set) from the informer, falling back to
// a live list. ns == "" means every namespace.
func (s *Service) listBuildsForAdmission(ctx context.Context, ns, project, selector string) []*unstructured.Unstructured {
	if project != "" {
		selector += "," + kube.LabelProject + "=" + project
	}
	sel, err := labels.Parse(selector)
	if err != nil {
		return nil
	}
	if list, ok := s.Kube.Cache.ListFromCache(kube.GVRBuilds, ns, sel); ok {
		return list
	}
	raw, err := s.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil
	}
	out := make([]*unstructured.Unstructured, 0, len(raw.Items))
	for i := range raw.Items {
		out = append(out, &raw.Items[i])
	}
	return out
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

// occupiesServiceSlot reports whether b holds its service's one build
// slot: it carries no build-state label (dispatched, not finished), or it
// is a release-failed build re-running its release hook. The retry keeps
// the done label, so without the second case a push during the retry
// started a build whose migration ran concurrently with the retried one.
func occupiesServiceSlot(b *kube.KusoBuild) bool {
	if b.Labels[LabelBuildState] == "" {
		return true
	}
	return b.Annotations[annRetryRelease] != "" && buildPhase(b) == "running"
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
		if occupiesServiceSlot(&raw[i]) {
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
	var newerRef string
	for i := range raw {
		if raw[i].Name == newName {
			newerRef = raw[i].Spec.Ref
		}
	}
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
		s.deleteCloneTokenSecret(ns, name)
		bg := metav1.DeletePropagationBackground
		if jerr := s.Kube.Clientset.BatchV1().Jobs(ns).Delete(lctx, name, metav1.DeleteOptions{
			PropagationPolicy: &bg,
		}); jerr != nil && !apierrors.IsNotFound(jerr) {
			slog.Default().Warn("builds: delete superseded job", "err", jerr, "build", name)
		}
		if s.Notifier != nil {
			short := strings.TrimPrefix(fqn, project+"-")
			if raw[i].Annotations == nil {
				raw[i].Annotations = map[string]string{}
			}
			raw[i].Annotations[annCompletedAt] = now
			targets := lookupBuildTargets(lctx, s.Kube, ns, s.Namespace, &raw[i])
			title, desc, fields := buildRichCard(&raw[i], short, "superseded", "", targets)
			if desc == "" {
				desc = replacedByDescription(raw[i].Spec.Branch, newerRef)
			}
			s.Notifier.Emit(EventEnvelope{
				Type:        eventBuildSuperseded,
				Title:       title,
				Description: desc,
				Project:     project,
				Service:     short,
				Env:         singleTargetEnv(targets),
				URL:         buildEventURL(project, short),
				Severity:    "info",
				DurationMs:  buildDurationMs(&raw[i]),
				Fields:      fields,
				Links:       buildCardLinks(project, short, "superseded", targets, nil),
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
