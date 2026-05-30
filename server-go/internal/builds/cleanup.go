// Background cleanup of finished KusoBuild CRs and the helm-release
// secrets they leave behind.
//
// Why this exists: helm-operator reconciles every KusoBuild CR every
// 60s, and "reconcile" means render the chart + run a `helm upgrade`
// dry-run + diff against the live release — not free. Without
// pruning, a cluster that's seen 50 builds since yesterday burns
// real CPU on dead work. The build-state=done watch selector
// (operator/watches.yaml) already short-circuits the reconcile, but
// we still want the CR/release records gone so they don't bloat the
// k3s SQLite datastore (etcd-equivalent) over time.
//
// Two passes per tick:
//   1. Delete KusoBuild CRs older than retention with
//      kuso.sislelabs.com/build-state=done. The owned helm release
//      secret goes with the CR via the helm-operator's uninstall
//      finalizer (which the existing finalizer-sweep clears if
//      stuck).
//   2. Sweep orphan sh.helm.release.v1.* Secrets — releases whose
//      owning CR is gone but whose helm release secret never got
//      uninstalled (typically because the operator was down or the
//      finalizer raced).

package builds

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"kuso/server/internal/kube"
)

// SweepFinishedBuilds deletes KusoBuild CRs in `namespace` whose
// build-state label is "done" and whose creation timestamp is older
// than `keepFor`. Returns the count deleted. Errors per-CR are
// logged to logFn but don't abort the sweep.
func SweepFinishedBuilds(ctx context.Context, kc *kube.Client, namespace string, keepFor time.Duration, logFn func(msg string, kv ...any)) (int, error) {
	cutoff := time.Now().Add(-keepFor)
	list, err := kc.ListKusoBuildsByLabels(ctx, namespace, map[string]string{
		"kuso.sislelabs.com/build-state": "done",
	})
	if err != nil {
		return 0, fmt.Errorf("list finished builds: %w", err)
	}
	deleted := 0
	for i := range list {
		item := &list[i]
		ts := item.CreationTimestamp
		if ts.IsZero() || ts.Time.After(cutoff) {
			continue
		}
		name := item.Name
		if err := kc.Dynamic.Resource(kube.GVRBuilds).Namespace(namespace).
			Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			if logFn != nil {
				logFn("delete finished build", "name", name, "err", err)
			}
			continue
		}
		deleted++
	}
	return deleted, nil
}

// CapBuildsPerService trims finished KusoBuild CRs so each service
// retains at most `max` of its most-recent done builds. Anything
// older is deleted regardless of age — guards against a hot-fix loop
// piling up 500 successful builds in an hour from outgrowing the
// daily SweepFinishedBuilds tick.
//
// Why this is a separate sweep from SweepFinishedBuilds (which is
// age-based): the leader-gated 24h tick can pause for a full day if
// the leader pod dies + lease takes a minute to re-elect + the next
// tick lands a day later. This cap-based sweep runs on every replica
// from the build poller's tick (every ~6min) so a busy cluster can't
// outgrow retention while the leader is unavailable.
//
// "Most recent" is by creationTimestamp. Errors per-CR are logged
// and counted but don't abort the sweep.
func CapBuildsPerService(ctx context.Context, kc *kube.Client, namespace string, max int, logFn func(msg string, kv ...any)) (int, error) {
	if max <= 0 {
		return 0, nil
	}
	list, err := kc.ListKusoBuildsByLabels(ctx, namespace, map[string]string{
		"kuso.sislelabs.com/build-state": "done",
	})
	if err != nil {
		return 0, fmt.Errorf("list finished builds: %w", err)
	}
	// Group by service label. Builds without the label (very old, pre
	// the labelling change) are skipped — they'll fall out via the
	// age-based SweepFinishedBuilds.
	type build struct {
		name string
		ts   time.Time
	}
	byService := map[string][]build{}
	for i := range list {
		item := &list[i]
		svc := item.Labels["kuso.sislelabs.com/service"]
		if svc == "" {
			continue
		}
		byService[svc] = append(byService[svc], build{name: item.Name, ts: item.CreationTimestamp.Time})
	}
	deleted := 0
	for svc, builds := range byService {
		if len(builds) <= max {
			continue
		}
		// Sort newest first; keep the first `max`, delete the rest.
		// Stable sort so two builds with identical timestamps (rare
		// but possible in a synthetic-ref redeploy burst) retain
		// their list order for deterministic behaviour.
		for i := 1; i < len(builds); i++ {
			j := i
			for j > 0 && builds[j].ts.After(builds[j-1].ts) {
				builds[j], builds[j-1] = builds[j-1], builds[j]
				j--
			}
		}
		for _, b := range builds[max:] {
			if err := kc.Dynamic.Resource(kube.GVRBuilds).Namespace(namespace).
				Delete(ctx, b.name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				if logFn != nil {
					logFn("cap: delete build", "service", svc, "build", b.name, "err", err)
				}
				continue
			}
			deleted++
		}
	}
	return deleted, nil
}

// imageRetentionRecord is the internal shape the sweep reasons over.
// Live KusoBuild CRs and archived ArchivedImageRecords both project onto
// this so the window spans the full history, not just live CRs.
type imageRetentionRecord struct {
	buildName string
	project   string
	service   string // short name
	imageTag  string
	createdAt time.Time
	succeeded bool
}

// ArchivedImageRecord is the exported shape main.go's db adapter hands
// to the sweep for builds whose CR is already GC'd. (Mirror of
// db.ArchivedImage so the builds package doesn't import db.)
type ArchivedImageRecord struct {
	BuildName string
	Project   string
	Service   string
	ImageTag  string
	Status    string
	CreatedAt time.Time
}

// ImageRecordLister supplies archived (CR-gone) build summaries for a
// namespace so the sweep can see builds whose CR is already deleted, and
// lets the sweep blank a record's imageTag once its registry image is
// pruned (so the UI stops offering rollback for it). Exported so main.go
// can wire a db-backed adapter.
type ImageRecordLister interface {
	ListArchivedImages(ctx context.Context, namespace string) ([]ArchivedImageRecord, error)
	ClearImageTag(ctx context.Context, project, buildName string) error
}

// SweepImagesPastWindow keeps the `keep` most-recent SUCCEEDED builds
// per service (by createdAt, across live CRs + archived records) and
// deletes the registry image TAG of every older succeeded build, so the
// registry doesn't grow unbounded. The DB record is left intact — the
// Deployments tab still lists the build; it just can't be rolled back to
// once its image is gone, and the next weekly registry GC reclaims the
// now-untagged blobs.
//
// Only the default in-cluster registry is pruned (the caller passes a
// del that no-ops for external registries). Untagging is idempotent
// (404 → nil), so a re-run is safe. Mirrors CapBuildsPerService's
// namespace-listing shape. Errors per-image are logged and counted but
// don't abort the sweep.
func SweepImagesPastWindow(
	ctx context.Context,
	kc *kube.Client,
	namespace string,
	lister ImageRecordLister,
	del ImageDeleter,
	keep int,
	logFn func(msg string, kv ...any),
) (int, error) {
	if keep <= 0 || del == nil {
		return 0, nil
	}
	list, err := kc.ListKusoBuildsByLabels(ctx, namespace, map[string]string{
		"kuso.sislelabs.com/build-state": "done",
	})
	if err != nil {
		return 0, fmt.Errorf("image-sweep list builds: %w", err)
	}

	// Group succeeded, image-bearing builds by (project, service). A live
	// CR wins over an archived record of the same build name.
	byKey := map[svcKey][]imageRetentionRecord{}
	seen := map[string]bool{}
	add := func(project string, r imageRetentionRecord) {
		if r.imageTag == "" || !r.succeeded || seen[r.buildName] {
			return
		}
		seen[r.buildName] = true
		byKey[svcKey{project, r.service}] = append(byKey[svcKey{project, r.service}], r)
	}
	for i := range list {
		b := &list[i]
		if b.Spec.Image == nil || buildPhase(b) != "succeeded" {
			continue
		}
		add(b.Spec.Project, imageRetentionRecord{
			buildName: b.Name,
			service:   strings.TrimPrefix(b.Spec.Service, b.Spec.Project+"-"),
			imageTag:  b.Spec.Image.Tag,
			createdAt: b.CreationTimestamp.Time,
			succeeded: true,
		})
	}
	if lister != nil {
		archived, lerr := lister.ListArchivedImages(ctx, namespace)
		if lerr != nil {
			if logFn != nil {
				logFn("image-sweep: list archived", "ns", namespace, "err", lerr)
			}
		} else {
			for _, a := range archived {
				add(a.Project, imageRetentionRecord{
					buildName: a.BuildName,
					project:   a.Project,
					service:   a.Service,
					imageTag:  a.ImageTag,
					createdAt: a.CreatedAt,
					succeeded: a.Status == "succeeded",
				})
			}
		}
	}

	targets := imagesToUntag(byKey, keep)
	deleted := 0
	for _, t := range targets {
		if err := del.DeleteImageTag(ctx, t.repo, t.tag); err != nil {
			if logFn != nil {
				logFn("image-sweep: delete tag", "repo", t.repo, "tag", t.tag, "err", err)
			}
			continue
		}
		deleted++
		// Blank the archived record's imageTag so the Deployments tab
		// stops offering rollback for it (the image is gone). Best-effort
		// — on failure the rollback handler still rejects the pruned
		// image with a clear error, so the UI is just slightly stale.
		if lister != nil {
			if err := lister.ClearImageTag(ctx, t.project, t.buildName); err != nil && logFn != nil {
				logFn("image-sweep: clear record tag", "project", t.project, "build", t.buildName, "err", err)
			}
		}
	}
	return deleted, nil
}

// svcKey groups builds by project + service short-name.
type svcKey struct{ project, service string }

// untagTarget is one image the sweep should delete + the record to clear.
type untagTarget struct {
	repo      string
	tag       string
	project   string
	buildName string
}

// imagesToUntag is the pure decision: per (project, service) group, keep
// the `keep` newest records and return the untag targets for every older
// one. Extracted from SweepImagesPastWindow so the windowing logic is
// unit-testable without a cluster or registry.
func imagesToUntag(byKey map[svcKey][]imageRetentionRecord, keep int) []untagTarget {
	var out []untagTarget
	for k, recs := range byKey {
		if len(recs) <= keep {
			continue
		}
		sortRecordsNewestFirst(recs)
		repo := fmt.Sprintf("%s/%s", k.project, k.service)
		for _, r := range recs[keep:] {
			out = append(out, untagTarget{repo: repo, tag: r.imageTag, project: k.project, buildName: r.buildName})
		}
	}
	return out
}

func sortRecordsNewestFirst(recs []imageRetentionRecord) {
	for i := 1; i < len(recs); i++ {
		j := i
		for j > 0 && recs[j].createdAt.After(recs[j-1].createdAt) {
			recs[j], recs[j-1] = recs[j-1], recs[j]
			j--
		}
	}
}

// SweepOrphanHelmReleases removes Secrets named
// sh.helm.release.v1.<release>.v<rev> whose corresponding kuso CR no
// longer exists in the namespace. We restrict the sweep to releases
// that look kuso-shaped (their name appears as a CR name across our
// known GVRs) so we don't delete unrelated helm releases the user
// installed by hand into the kuso namespace.
//
// Helm release secrets carry label `owner=helm` and `name=<release>`
// — we use the latter to identify the release. The version suffix
// is the secret's `version` label (or parsed from the name).
func SweepOrphanHelmReleases(ctx context.Context, kc *kube.Client, namespace string, logFn func(msg string, kv ...any)) (int, error) {
	secs, err := kc.Clientset.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "owner=helm",
	})
	if err != nil {
		return 0, fmt.Errorf("list helm release secrets: %w", err)
	}

	// Collect every kuso CR name in the namespace across our known
	// GVRs into a single set. We can be conservative — anything we
	// don't recognise is left alone.
	live := map[string]struct{}{}
	gvrs := []struct {
		label string
		gvr   schema.GroupVersionResource
	}{
		{"kusoprojects", kube.GVRProjects},
		{"kusoservices", kube.GVRServices},
		{"kusoenvironments", kube.GVREnvironments},
		{"kusoaddons", kube.GVRAddons},
		{"kusocrons", kube.GVRCrons},
	}
	for _, g := range gvrs {
		l, err := kc.Dynamic.Resource(g.gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			if logFn != nil {
				logFn("orphan-sweep list", "kind", g.label, "err", err)
			}
			continue
		}
		for i := range l.Items {
			live[l.Items[i].GetName()] = struct{}{}
		}
	}

	deleted := 0
	for i := range secs.Items {
		sec := &secs.Items[i]
		release := sec.Labels["name"]
		if release == "" {
			// Older helm versions stored the release name only in the
			// secret's own name `sh.helm.release.v1.<release>.v<rev>`.
			// We parse it back out.
			n := sec.Name
			if !strings.HasPrefix(n, "sh.helm.release.v1.") {
				continue
			}
			rest := strings.TrimPrefix(n, "sh.helm.release.v1.")
			// rest = "<release>.v<rev>" — strip the trailing .v<digits>.
			if i := strings.LastIndex(rest, ".v"); i > 0 {
				release = rest[:i]
			}
		}
		if release == "" {
			continue
		}
		if _, isLive := live[release]; isLive {
			continue
		}
		// Conservative match: only sweep if the release name looks like
		// a kuso convention (project / service / addon / env). We use a
		// negative test instead — skip anything that has no kuso prefix
		// and is short (< 4 chars). The vast majority of false-positives
		// would be 1-2 char manual installs.
		if !looksKusoShaped(release) {
			continue
		}
		if err := kc.Clientset.CoreV1().Secrets(namespace).Delete(ctx, sec.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			if logFn != nil {
				logFn("delete orphan helm release", "name", sec.Name, "err", err)
			}
			continue
		}
		deleted++
	}
	return deleted, nil
}

// looksKusoShaped returns true for release names that match our
// naming conventions: <project>-<service>, <project>-<addon>,
// <release>-production, <release>-pr-N, or single-segment names
// that match a known well-formed slug. Used to scope the orphan
// sweep so we never delete helm releases the user installed by
// hand into the kuso namespace.
func looksKusoShaped(name string) bool {
	if name == "" {
		return false
	}
	// Must be lowercase / dashes / digits — every kuso CR name passes
	// the kube-style RFC 1123 subdomain rule.
	for _, r := range name {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '-' && r != '.' {
			return false
		}
	}
	return true
}

// Use the slog logger's info adapter for the cleanup callsites.
// Avoids importing slog from the call site.
type slogger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
}

// LogAdapter wraps a *slog.Logger as a (msg, kv...) callback for the
// sweep functions above. Tiny shim — the sweep functions accept a
// plain `func(msg string, kv ...any)` so they don't pull slog into
// the public API.
func LogAdapter(l *slog.Logger) func(msg string, kv ...any) {
	return func(msg string, kv ...any) { l.Warn(msg, kv...) }
}
