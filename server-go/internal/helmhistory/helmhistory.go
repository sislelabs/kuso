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
// It never touches the newest revisions, a revision whose status is
// deployed or pending-*, or anything not labelled owner=helm, so the
// release stays installed and an in-flight upgrade is left alone.
package helmhistory

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/metadata"
)

// DefaultKeep matches `helm upgrade --history-max`'s default of 10.
const DefaultKeep = 10

var secretsGVR = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}

// A long-lived install can hold thousands of stale revisions for one
// release. The first sweeps work through them gradually instead of
// issuing every DELETE at once against kine and the client's QPS budget.
const (
	maxDeletesPerNamespace = 1000
	deleteGap              = 50 * time.Millisecond
)

// Pruner deletes superseded helm release revisions.
type Pruner struct {
	// Meta lists Secret metadata only: release Secrets carry the whole
	// gzipped manifest, so a full LIST would pull megabytes per sweep.
	Meta       metadata.Interface
	Keep       int
	Namespaces func(context.Context) []string
	Interval   time.Duration
	Logger     *slog.Logger

	noPacing bool // tests
}

// Run sweeps once shortly after start, then every Interval.
func (p *Pruner) Run(ctx context.Context) {
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
			p.Logger.Info("helm history pruned", "namespace", ns, "deleted", n, "keep", p.keep())
		}
	}
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

// PruneNamespace deletes the revisions beyond the newest Keep of every
// helm release in ns and returns how many it deleted.
func (p *Pruner) PruneNamespace(ctx context.Context, ns string) (int, error) {
	lctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	list, err := p.Meta.Resource(secretsGVR).Namespace(ns).List(lctx, metav1.ListOptions{LabelSelector: "owner=helm"})
	cancel()
	if err != nil {
		return 0, fmt.Errorf("list helm release secrets in %s: %w", ns, err)
	}
	byRelease := map[string][]revision{}
	for _, it := range list.Items {
		l := it.Labels
		v, err := strconv.Atoi(l["version"])
		if err != nil || l["name"] == "" || !strings.HasPrefix(it.Name, "sh.helm.release.v1.") {
			continue
		}
		byRelease[l["name"]] = append(byRelease[l["name"]], revision{name: it.Name, version: v, status: l["status"]})
	}
	deleted := 0
	var firstErr error
	for _, revs := range byRelease {
		for _, r := range prunable(revs, p.keep()) {
			if deleted >= maxDeletesPerNamespace || ctx.Err() != nil {
				return deleted, firstErr
			}
			if deleted > 0 && p.gap() > 0 {
				select {
				case <-ctx.Done():
					return deleted, firstErr
				case <-time.After(p.gap()):
				}
			}
			if err := p.Meta.Resource(secretsGVR).Namespace(ns).Delete(ctx, r.name, metav1.DeleteOptions{}); err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("delete %s/%s: %w", ns, r.name, err)
				}
				continue
			}
			deleted++
		}
	}
	return deleted, firstErr
}

// prunable returns the revisions to delete: everything older than the
// newest keep, minus any deployed or pending revision.
func prunable(revs []revision, keep int) []revision {
	if len(revs) <= keep {
		return nil
	}
	sort.Slice(revs, func(i, j int) bool { return revs[i].version > revs[j].version })
	var out []revision
	for _, r := range revs[keep:] {
		if r.status == "deployed" || strings.HasPrefix(r.status, "pending") {
			continue
		}
		out = append(out, r)
	}
	return out
}
