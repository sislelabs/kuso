// Orphan-repository sweep for the in-cluster registry.
//
// SweepImagesPastWindow only prunes services that still exist: it walks
// build records per (project, service). When a project or service is
// deleted its registry repository is never untagged again, so the weekly
// `registry garbage-collect --delete-untagged` can never free its blobs.
// This sweep untags every manifest in a `<project>/<service>` repository
// whose KusoProject or KusoService no longer exists anywhere in the
// cluster; the weekly GC then reclaims the space.

package builds

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"kuso/server/internal/kube"
)

// OrphanSweepMode is KUSO_REGISTRY_ORPHAN_SWEEP: off, dryrun (log what
// would be deleted) or on.
type OrphanSweepMode string

const (
	OrphanSweepOff    OrphanSweepMode = "off"
	OrphanSweepDryRun OrphanSweepMode = "dryrun"
	OrphanSweepOn     OrphanSweepMode = "on"
)

// ParseOrphanSweepMode maps the env value to a mode. Anything other than
// an explicit "on" or "off" is dryrun, so a typo never enables deletion.
func ParseOrphanSweepMode(v string) OrphanSweepMode {
	switch OrphanSweepMode(strings.ToLower(strings.TrimSpace(v))) {
	case OrphanSweepOn:
		return OrphanSweepOn
	case OrphanSweepOff:
		return OrphanSweepOff
	default:
		return OrphanSweepDryRun
	}
}

// OrphanSweepGrace is how recent a repo's newest image may be before the
// sweep leaves the repo alone.
const OrphanSweepGrace = 7 * 24 * time.Hour

// OrphanSweepResult is the sweep's summary. Manifests counts deletions,
// or would-be deletions in dry-run.
type OrphanSweepResult struct {
	Mode            OrphanSweepMode
	ReposScanned    int
	ReposOrphaned   int
	ReposSwept      int
	Manifests       int
	ManifestsKept   int // referenced by a live env/cron/run/pod
	ReposTooYoung   int
	ReposBuilding   int
	ReposSkippedErr int
}

// SweepOrphanRepositories untags every manifest in registry repositories
// whose project or service no longer exists. Every kube read is a live,
// cluster-wide LIST (not the informer cache), and any failed read aborts
// the whole sweep: a skipped sweep is recoverable, deleting an image
// something still runs is not.
func SweepOrphanRepositories(ctx context.Context, kc *kube.Client, reg RegistryInventory, mode OrphanSweepMode, grace time.Duration, now time.Time, logger *slog.Logger) (OrphanSweepResult, error) {
	res := OrphanSweepResult{Mode: mode}
	if mode == OrphanSweepOff || reg == nil || kc == nil {
		return res, nil
	}
	if logger == nil {
		logger = slog.Default()
	}

	before, err := listRepoOwners(ctx, kc)
	if err != nil {
		return res, err
	}
	repos, err := reg.ListRepositories(ctx)
	if err != nil {
		return res, fmt.Errorf("orphan-sweep catalog: %w", err)
	}
	res.ReposScanned = len(repos)
	var candidates []string
	for _, r := range repos {
		if before.orphaned(r) {
			candidates = append(candidates, r)
		}
	}
	if len(candidates) == 0 {
		return res, nil
	}

	// Read the protections and re-read the owners AFTER the catalog, so a
	// project created (or a pod started) while we enumerated is seen. A
	// repo is only orphaned if its owner was absent on both reads.
	prot, err := listProtectedImages(ctx, kc)
	if err != nil {
		return res, err
	}
	after, err := listRepoOwners(ctx, kc)
	if err != nil {
		return res, err
	}
	sort.Strings(candidates)
	for _, repo := range candidates {
		if !after.orphaned(repo) {
			continue
		}
		res.ReposOrphaned++
		if prot.repos[repo] {
			res.ReposBuilding++
			logger.Info("registry orphan-sweep: repo has an unfinished build, skipped", "repo", repo)
			continue
		}
		deleted, kept, young, err := sweepOrphanRepo(ctx, reg, repo, prot, mode, now.Add(-grace), logger)
		switch {
		case err != nil:
			res.ReposSkippedErr++
			logger.Warn("registry orphan-sweep: repo skipped", "repo", repo, "err", err)
		case young:
			res.ReposTooYoung++
		default:
			res.Manifests += deleted
			res.ManifestsKept += kept
			if deleted > 0 {
				res.ReposSwept++
			}
		}
	}
	return res, nil
}

// sweepOrphanRepo deletes (or, in dry-run, logs) every manifest in repo
// that no protected reference resolves to. young reports a repo skipped
// for holding an image built after cutoff.
func sweepOrphanRepo(ctx context.Context, reg RegistryInventory, repo string, prot protectedImages, mode OrphanSweepMode, cutoff time.Time, logger *slog.Logger) (deleted, kept int, young bool, err error) {
	tags, err := reg.ListTags(ctx, repo)
	if err != nil {
		return 0, 0, false, fmt.Errorf("list tags: %w", err)
	}
	// Resolve every tag up front: deletion is by digest and removes the
	// manifest for every tag sharing it, so one protected tag must keep
	// the digest for all of them.
	tagsByDigest := map[string][]string{}
	keep := map[string]bool{}
	for _, tag := range tags {
		dg, err := reg.TagDigest(ctx, repo, tag)
		if err != nil {
			return 0, 0, false, fmt.Errorf("resolve %s: %w", tag, err)
		}
		if dg == "" {
			continue
		}
		tagsByDigest[dg] = append(tagsByDigest[dg], tag)
		if prot.tags[repo+":"+tag] || prot.digests[repo+"@"+dg] {
			keep[dg] = true
		}
	}
	digests := make([]string, 0, len(tagsByDigest))
	for dg := range tagsByDigest {
		digests = append(digests, dg)
	}
	sort.Strings(digests)
	for _, dg := range digests {
		created, err := reg.ImageCreated(ctx, repo, dg)
		if err != nil {
			return 0, 0, false, fmt.Errorf("date %s: %w", dg, err)
		}
		if created.After(cutoff) {
			logger.Info("registry orphan-sweep: repo has a recent image, skipped", "repo", repo, "created", created)
			return 0, 0, true, nil
		}
	}
	for _, dg := range digests {
		if keep[dg] {
			kept++
			logger.Info("registry orphan-sweep: manifest still referenced, kept", "repo", repo, "digest", dg, "tags", tagsByDigest[dg])
			continue
		}
		if mode != OrphanSweepOn {
			deleted++
			logger.Info("registry orphan-sweep: would delete", "repo", repo, "digest", dg, "tags", tagsByDigest[dg])
			continue
		}
		if err := reg.DeleteManifest(ctx, repo, dg); err != nil {
			logger.Warn("registry orphan-sweep: delete", "repo", repo, "digest", dg, "err", err)
			continue
		}
		deleted++
	}
	return deleted, kept, false, nil
}

// repoOwners is the set of projects and "<project>/<service>" repos that
// currently exist.
type repoOwners struct {
	projects map[string]bool
	services map[string]bool
}

// orphaned reports whether repo is a kuso-shaped "<project>/<service>"
// repository with no owning project or service. Other shapes are never
// orphaned: kuso didn't push them.
func (o repoOwners) orphaned(repo string) bool {
	project, service, ok := strings.Cut(repo, "/")
	if !ok || project == "" || service == "" || strings.Contains(service, "/") {
		return false
	}
	return !o.projects[project] || !o.services[repo]
}

func listRepoOwners(ctx context.Context, kc *kube.Client) (repoOwners, error) {
	o := repoOwners{projects: map[string]bool{}, services: map[string]bool{}}
	projects, err := liveListAll[kube.KusoProject](ctx, kc, kube.GVRProjects)
	if err != nil {
		return o, err
	}
	for i := range projects {
		o.projects[projects[i].Name] = true
	}
	services, err := liveListAll[kube.KusoService](ctx, kc, kube.GVRServices)
	if err != nil {
		return o, err
	}
	for i := range services {
		p := services[i].Spec.Project
		o.services[p+"/"+strings.TrimPrefix(services[i].Name, p+"-")] = true
	}
	return o, nil
}

// protectedImages holds every image reference the sweep must not delete,
// keyed by the last two path segments of the repository, so the
// registry host spelling (in-cluster DNS, node mirror) doesn't matter.
type protectedImages struct {
	tags    map[string]bool // "<project>/<service>:<tag>"
	digests map[string]bool // "<project>/<service>@<digest>"
	repos   map[string]bool // repos with an unfinished build: left whole
}

func (p protectedImages) addKuso(img *kube.KusoImage) {
	if img == nil {
		return
	}
	if repo := repoKey(img.Repository); repo != "" && img.Tag != "" {
		p.tags[repo+":"+img.Tag] = true
	}
}

func (p protectedImages) addRef(ref string) {
	repo, tag, digest := parseImageRef(ref)
	if repo == "" {
		return
	}
	if tag != "" {
		p.tags[repo+":"+tag] = true
	}
	if digest != "" {
		p.digests[repo+"@"+digest] = true
	}
}

func listProtectedImages(ctx context.Context, kc *kube.Client) (protectedImages, error) {
	p := protectedImages{tags: map[string]bool{}, digests: map[string]bool{}, repos: map[string]bool{}}
	envs, err := liveListAll[kube.KusoEnvironment](ctx, kc, kube.GVREnvironments)
	if err != nil {
		return p, err
	}
	for i := range envs {
		p.addKuso(envs[i].Spec.Image)
		p.addKuso(envs[i].Spec.PendingImage)
	}
	crons, err := liveListAll[kube.KusoCron](ctx, kc, kube.GVRCrons)
	if err != nil {
		return p, err
	}
	for i := range crons {
		p.addKuso(crons[i].Spec.Image)
	}
	runs, err := liveListAll[kube.KusoRun](ctx, kc, kube.GVRRuns)
	if err != nil {
		return p, err
	}
	for i := range runs {
		p.addKuso(runs[i].Spec.Image)
	}
	builds, err := liveListAll[kube.KusoBuild](ctx, kc, kube.GVRBuilds)
	if err != nil {
		return p, err
	}
	for i := range builds {
		b := &builds[i]
		if buildTerminal(b) {
			continue
		}
		p.addKuso(b.Spec.Image)
		if b.Spec.Project != "" && b.Spec.Service != "" {
			p.repos[b.Spec.Project+"/"+strings.TrimPrefix(b.Spec.Service, b.Spec.Project+"-")] = true
		}
	}
	pods, err := kc.Clientset.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return p, fmt.Errorf("orphan-sweep list pods: %w", err)
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		for _, c := range pod.Spec.InitContainers {
			p.addRef(c.Image)
		}
		for _, c := range pod.Spec.Containers {
			p.addRef(c.Image)
		}
		for _, c := range pod.Spec.EphemeralContainers {
			p.addRef(c.Image)
		}
		for _, st := range pod.Status.InitContainerStatuses {
			p.addRef(st.ImageID)
		}
		for _, st := range pod.Status.ContainerStatuses {
			p.addRef(st.ImageID)
		}
	}
	return p, nil
}

// buildTerminal reports a build that can no longer push or promote an
// image. A "succeeded" build without the done mark may still be
// promoting, so it counts as unfinished.
func buildTerminal(b *kube.KusoBuild) bool {
	if b.Labels["kuso.sislelabs.com/build-state"] == "done" || b.Spec.Done {
		return true
	}
	switch buildPhase(b) {
	case "failed", "cancelled":
		return true
	}
	return false
}

// repoKey reduces an image repository to its last two path segments
// ("<project>/<service>"), or "" when it has fewer.
func repoKey(repository string) string {
	parts := strings.Split(repository, "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-2] + "/" + parts[len(parts)-1]
}

// parseImageRef splits a pod image or imageID ("host:5000/p/s:tag",
// "host/p/s@sha256:…", "docker-pullable://host/p/s@sha256:…") into its
// repo key, tag and digest.
func parseImageRef(ref string) (repo, tag, digest string) {
	if i := strings.Index(ref, "://"); i >= 0 {
		ref = ref[i+3:]
	}
	if i := strings.Index(ref, "@"); i >= 0 {
		ref, digest = ref[:i], ref[i+1:]
	}
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		ref, tag = ref[:i], ref[i+1:]
	}
	return repoKey(ref), tag, digest
}

// liveListAll lists gvr across every namespace straight from the
// apiserver. The informer cache is deliberately bypassed: a lagging or
// partially synced cache would make live objects look absent.
func liveListAll[T any](ctx context.Context, kc *kube.Client, gvr schema.GroupVersionResource) ([]T, error) {
	raw, err := kc.Dynamic.Resource(gvr).Namespace(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("orphan-sweep list %s: %w", gvr.Resource, err)
	}
	out := make([]T, len(raw.Items))
	for i := range raw.Items {
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw.Items[i].Object, &out[i]); err != nil {
			return nil, fmt.Errorf("orphan-sweep decode %s %s: %w", gvr.Resource, raw.Items[i].GetName(), err)
		}
	}
	return out, nil
}
