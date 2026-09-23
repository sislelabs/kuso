package runs

import (
	"context"
	"sort"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"kuso/server/internal/kube"
)

const (
	// DefaultRunRetention is how long a finished run outlives its
	// completion before GC may delete it (KUSO_RUN_RETENTION_DAYS).
	DefaultRunRetention = 7 * 24 * time.Hour
	// runGCKeep runs per service are always kept, whatever their age,
	// so the run history in the UI/CLI never empties out.
	runGCKeep = 20
	// runGCBatch caps deletes per GC pass. Each delete makes the
	// helm-operator uninstall a release (via its uninstall-release
	// finalizer), so a backlog drains over several passes instead of
	// flooding the operator.
	runGCBatch    = 25
	runGCInterval = 6 * time.Minute
)

// sweepFinishedRuns deletes up to runGCBatch terminal KusoRuns that are
// older than the retention window and outside their service's newest
// runGCKeep, oldest first. Runs that are not terminal are never deleted.
func (p *Poller) sweepFinishedRuns(ctx context.Context, now time.Time) int {
	retention := p.RunRetention
	if retention <= 0 {
		retention = DefaultRunRetention
	}
	cutoff := now.Add(-retention)

	type candidate struct {
		ns      string
		name    string
		created time.Time
	}
	var expired []candidate
	for _, ns := range p.scanNamespaces(ctx) {
		runs, err := p.Svc.Kube.ListKusoRuns(ctx, ns)
		if err != nil {
			p.logger().Warn("runs gc: list", "ns", ns, "err", err)
			continue
		}
		byService := map[string][]*kube.KusoRun{}
		for i := range runs {
			r := &runs[i]
			key := r.Spec.Project + "/" + r.Spec.Service
			byService[key] = append(byService[key], r)
		}
		for _, group := range byService {
			sort.Slice(group, func(i, j int) bool {
				return group[i].CreationTimestamp.After(group[j].CreationTimestamp.Time)
			})
			if len(group) <= runGCKeep {
				continue
			}
			for _, r := range group[runGCKeep:] {
				if !isTerminal(r.Annotations[annRunPhase]) || !r.CreationTimestamp.Time.Before(cutoff) {
					continue
				}
				expired = append(expired, candidate{ns: ns, name: r.Name, created: r.CreationTimestamp.Time})
			}
		}
	}
	sort.Slice(expired, func(i, j int) bool { return expired[i].created.Before(expired[j].created) })

	deleted := 0
	for _, c := range expired {
		if deleted >= runGCBatch || ctx.Err() != nil {
			break
		}
		if err := p.Svc.Kube.DeleteKusoRun(ctx, c.ns, c.name); err != nil && !apierrors.IsNotFound(err) {
			p.logger().Warn("runs gc: delete", "run", c.name, "ns", c.ns, "err", err)
			continue
		}
		deleted++
	}
	if deleted > 0 {
		p.logger().Info("runs gc: deleted finished runs", "count", deleted, "backlog", len(expired)-deleted)
	}
	return deleted
}
