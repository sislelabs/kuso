package builds

import (
	"context"
	"sort"

	"kuso/server/internal/kube"
)

// QueuePositions returns each queued build's 1-based place in the
// cluster-wide build queue, keyed by CR name. "Place" is ordered by
// CR creation time (oldest = #1, name as tiebreak) across every
// execution namespace — the same CRs the dispatcher considers for
// promotion (label build-state=queued).
//
// This is a user-facing approximation, not a promotion guarantee: the
// dispatcher promotes round-robin across projects (FIFO within a
// service), so a #3 in a quiet project can start before a #2 in a
// busy one. What the number gives users is an honest "how many builds
// are ahead of mine" that shrinks as the queue drains.
//
// Best-effort: a namespace whose list fails is skipped (positions
// within the remaining set stay consistent). Callers treat a missing
// entry as "unknown" and omit the position.
func (s *Service) QueuePositions(ctx context.Context) map[string]int {
	var queued []kube.KusoBuild
	for _, ns := range s.ScanNamespaces(ctx) {
		raw, err := s.Kube.ListKusoBuildsByLabels(ctx, ns, map[string]string{
			"kuso.sislelabs.com/build-state": "queued",
		})
		if err != nil {
			continue
		}
		queued = append(queued, raw...)
	}
	sort.SliceStable(queued, func(i, j int) bool {
		ti, tj := queued[i].CreationTimestamp, queued[j].CreationTimestamp
		if ti.Equal(&tj) {
			return queued[i].Name < queued[j].Name
		}
		return ti.Before(&tj)
	})
	out := make(map[string]int, len(queued))
	for i := range queued {
		out[queued[i].Name] = i + 1
	}
	return out
}
