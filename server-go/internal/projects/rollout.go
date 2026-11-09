package projects

import (
	"context"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"

	"kuso/server/internal/rollout"
)

// probeCache bounds the event lookups a failing rollout costs: one
// field-selected LIST per failing pod per probeCacheTTL, and only while
// the rollup has already decided the rollout fails its health check.
type probeCacheEntry struct {
	outcome string
	at      time.Time
}

const probeCacheTTL = 20 * time.Second

var probeCache sync.Map // ns/pod -> probeCacheEntry

func (s *Service) probeFailure(ctx context.Context, ns, pod string) string {
	if s.Kube == nil || s.Kube.Clientset == nil || pod == "" {
		return ""
	}
	key := ns + "/" + pod
	if v, ok := probeCache.Load(key); ok {
		if e := v.(probeCacheEntry); time.Since(e.at) < probeCacheTTL {
			return e.outcome
		}
	}
	sel := fields.Set{"involvedObject.kind": "Pod", "involvedObject.name": pod, "reason": "Unhealthy"}.AsSelector().String()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	outcome := ""
	if list, err := s.Kube.Clientset.CoreV1().Events(ns).List(ctx, metav1.ListOptions{FieldSelector: sel}); err == nil {
		var newest time.Time
		for i := range list.Items {
			ev := &list.Items[i]
			t := ev.LastTimestamp.Time
			if t.IsZero() {
				t = ev.EventTime.Time
			}
			if o := rollout.ProbeOutcome(ev.Message); o != "" && !t.Before(newest) {
				newest, outcome = t, o
			}
		}
	}
	probeCache.Store(key, probeCacheEntry{outcome: outcome, at: time.Now()})
	return outcome
}

// rolloutStatus is the wire shape of status.rollout (additive).
func rolloutStatus(f rollout.Facts, hcPath, probe string) map[string]any {
	out := map[string]any{
		"failing":         f.Failing,
		"previousServing": f.PreviousServing,
	}
	if f.Kind != "" {
		out["kind"] = f.Kind
	}
	if f.Cause != "" {
		out["cause"] = f.Cause
	}
	if f.Kind == rollout.KindHealthcheck && hcPath != "" {
		out["healthcheckPath"] = hcPath
		if probe != "" {
			out["probeResult"] = probe
		}
	}
	if !f.Since.IsZero() {
		out["since"] = f.Since.UTC().Format(time.RFC3339)
	}
	if !f.PreviousSince.IsZero() {
		out["previousSince"] = f.PreviousSince.UTC().Format(time.RFC3339)
	}
	return out
}
