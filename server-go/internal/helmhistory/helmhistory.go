// Package helmhistory prunes old helm release revisions.
//
// The helm-operator (operator-sdk v1.42) never sets Helm's MaxHistory:
// it has no flag for it and its action config leaves storage history
// unlimited, so every `helm upgrade` leaves another
// sh.helm.release.v1.<release>.v<N> Secret behind (papelito-db reached
// revision 14021). Those Secrets grow kine/etcd and the apiserver watch
// cache without bound. This sweep does what Helm's own MaxHistory would
// do: per release, keep the newest Keep revisions and delete older ones.
//
// Losing a release's history makes helm-operator reinstall it (for an
// addon, a fresh StatefulSet render), so the rules are conservative:
//   - only Secrets labelled owner=helm whose name is exactly
//     sh.helm.release.v1.<name label>.v<version label>;
//   - a release with no revision in status "deployed" is skipped whole;
//   - the highest revision is never deleted, whatever its status;
//   - nor is any deployed or pending-* revision, nor the newest Keep;
//   - a paginated (incomplete) list aborts the namespace.
//
// Mode defaults to dryrun: it logs what it would delete and counts it in
// kuso_helm_history_prunable_revisions_total, deleting nothing.
package helmhistory

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/metadata"
)

// DefaultKeep matches `helm upgrade --history-max`'s default of 10.
const DefaultKeep = 10

// Mode is KUSO_HELM_HISTORY_PRUNE.
type Mode string

const (
	ModeOff    Mode = "off"
	ModeDryRun Mode = "dryrun"
	ModeOn     Mode = "on"
)

// ParseMode maps the env value to a Mode; anything unrecognised is
// dryrun, so a typo never turns deletion on.
func ParseMode(s string) Mode {
	switch Mode(strings.ToLower(strings.TrimSpace(s))) {
	case ModeOff:
		return ModeOff
	case ModeOn:
		return ModeOn
	default:
		return ModeDryRun
	}
}

var secretsGVR = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}

// A long-lived install can hold thousands of stale revisions for one
// release. The first sweeps work through them gradually instead of
// issuing every DELETE at once against kine and the client's QPS budget.
const (
	maxDeletesPerNamespace = 1000
	deleteGap              = 50 * time.Millisecond
)

var prunableRevisions = promauto.NewCounterVec(prometheus.CounterOpts{
	Namespace: "kuso",
	Subsystem: "helm_history",
	Name:      "prunable_revisions_total",
	Help:      "Old helm release revisions the history pruner found, by outcome (dryrun = would delete, deleted).",
}, []string{"outcome"})

// Pruner deletes superseded helm release revisions.
type Pruner struct {
	// Meta lists Secret metadata only: release Secrets carry the whole
	// gzipped manifest, so a full LIST would pull megabytes per sweep.
	Meta       metadata.Interface
	Keep       int
	Mode       Mode
	Namespaces func(context.Context) []string
	Interval   time.Duration
	Logger     *slog.Logger

	noPacing bool // tests
}

// Run sweeps once shortly after start, then every Interval.
func (p *Pruner) Run(ctx context.Context) {
	if p.Mode == ModeOff {
		return
	}
	if p.Interval <= 0 {
		p.Interval = 6 * time.Hour
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(2 * time.Minute):
	}
	t := time.NewTicker(p.Interval)
	defer t.Stop()
	for {
		p.sweep(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (p *Pruner) sweep(ctx context.Context) {
	lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	nss := p.Namespaces(lctx)
	cancel()
	for _, ns := range nss {
		n, err := p.PruneNamespace(ctx, ns)
		if err != nil {
			p.Logger.Warn("helm history prune", "namespace", ns, "err", err)
		}
		if n > 0 {
			p.Logger.Info("helm history pruned", "namespace", ns, "mode", p.mode(), "revisions", n, "keep", p.keep())
		}
	}
}

func (p *Pruner) mode() Mode {
	if p.Mode == "" {
		return ModeDryRun
	}
	return p.Mode
}

func (p *Pruner) gap() time.Duration {
	if p.noPacing {
		return 0
	}
	return deleteGap
}

func (p *Pruner) keep() int {
	if p.Keep < 1 {
		return DefaultKeep
	}
	return p.Keep
}

type revision struct {
	name    string
	version int
	status  string
}

// PruneNamespace finds the prunable revisions of every helm release in
// ns. In ModeOn it deletes them and returns how many it deleted; in
// dryrun it logs them per release and returns how many it would delete.
func (p *Pruner) PruneNamespace(ctx context.Context, ns string) (int, error) {
	if p.mode() == ModeOff {
		return 0, nil
	}
	lctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	list, err := p.Meta.Resource(secretsGVR).Namespace(ns).List(lctx, metav1.ListOptions{LabelSelector: "owner=helm"})
	cancel()
	if err != nil {
		return 0, fmt.Errorf("list helm release secrets in %s: %w", ns, err)
	}
	if list.Continue != "" {
		// A partial list could hide a release's deployed revision.
		return 0, fmt.Errorf("list helm release secrets in %s: paginated response, skipping namespace", ns)
	}
	byRelease := map[string][]revision{}
	for _, it := range list.Items {
		l := it.Labels
		rel := l["name"]
		v, err := strconv.Atoi(l["version"])
		if err != nil || rel == "" || it.Name != fmt.Sprintf("sh.helm.release.v1.%s.v%d", rel, v) {
			continue
		}
		byRelease[rel] = append(byRelease[rel], revision{name: it.Name, version: v, status: l["status"]})
	}
	releases := make([]string, 0, len(byRelease))
	for rel := range byRelease {
		releases = append(releases, rel)
	}
	sort.Strings(releases)

	count := 0
	var firstErr error
	for _, rel := range releases {
		victims := prunable(byRelease[rel], p.keep())
		if len(victims) == 0 {
			continue
		}
		if p.mode() == ModeDryRun {
			vs := make([]int, len(victims))
			for i, r := range victims {
				vs[i] = r.version
			}
			p.Logger.Info("helm history prune (dryrun): would delete revisions",
				"namespace", ns, "release", rel, "count", len(victims),
				"revisions", compactRanges(vs), "keep", p.keep())
			prunableRevisions.WithLabelValues("dryrun").Add(float64(len(victims)))
			count += len(victims)
			continue
		}
		for _, r := range victims {
			if count >= maxDeletesPerNamespace || ctx.Err() != nil {
				return count, firstErr
			}
			if count > 0 && p.gap() > 0 {
				select {
				case <-ctx.Done():
					return count, firstErr
				case <-time.After(p.gap()):
				}
			}
			if err := p.Meta.Resource(secretsGVR).Namespace(ns).Delete(ctx, r.name, metav1.DeleteOptions{}); err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("delete %s/%s: %w", ns, r.name, err)
				}
				continue
			}
			prunableRevisions.WithLabelValues("deleted").Inc()
			count++
		}
	}
	return count, firstErr
}

// prunable returns the revisions to delete: nothing for a release with no
// deployed revision; otherwise everything older than the newest keep,
// minus any deployed or pending revision and never the highest revision.
func prunable(revs []revision, keep int) []revision {
	if len(revs) <= keep {
		return nil
	}
	deployed := false
	for _, r := range revs {
		if r.status == "deployed" {
			deployed = true
			break
		}
	}
	if !deployed {
		return nil
	}
	sort.Slice(revs, func(i, j int) bool { return revs[i].version > revs[j].version })
	highest := revs[0].version
	var out []revision
	for _, r := range revs[max(keep, 1):] {
		if r.version == highest || r.status == "deployed" || strings.HasPrefix(r.status, "pending") {
			continue
		}
		out = append(out, r)
	}
	return out
}

// compactRanges renders sorted-descending versions as "1-5,7" ascending.
func compactRanges(vs []int) string {
	s := append([]int(nil), vs...)
	sort.Ints(s)
	var b strings.Builder
	for i := 0; i < len(s); {
		j := i
		for j+1 < len(s) && s[j+1] == s[j]+1 {
			j++
		}
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		if i == j {
			fmt.Fprintf(&b, "%d", s[i])
		} else {
			fmt.Fprintf(&b, "%d-%d", s[i], s[j])
		}
		i = j + 1
	}
	return b.String()
}
