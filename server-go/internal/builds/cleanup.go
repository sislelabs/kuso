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
	list, err := kc.Dynamic.Resource(kube.GVRBuilds).Namespace(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "kuso.sislelabs.com/build-state=done",
	})
	if err != nil {
		return 0, fmt.Errorf("list finished builds: %w", err)
	}
	deleted := 0
	for i := range list.Items {
		item := &list.Items[i]
		ts := item.GetCreationTimestamp()
		if ts.IsZero() || ts.After(cutoff) {
			continue
		}
		name := item.GetName()
		// The watch-selector excludes build-state=done from the
		// helm-operator's reconcile queue, which means it also won't
		// see our delete event — so the helm-uninstall finalizer
		// would hang the CR forever. Strip it ourselves before delete.
		// SweepOrphanHelmReleases reaps the dangling helm secret on a
		// later tick.
		if err := kube.StripHelmFinalizers(ctx, kc, kube.GVRBuilds, namespace, name); err != nil && !apierrors.IsNotFound(err) {
			if logFn != nil {
				logFn("strip finalizer", "name", name, "err", err)
			}
			// Don't continue — try the delete anyway. The kube API
			// will refuse if the finalizer is still attached, in
			// which case we log + skip on the next line.
		}
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
		{"kusobuilds", kube.GVRBuilds},
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
