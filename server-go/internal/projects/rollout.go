package projects

import (
	"context"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"

	"kuso/server/internal/rollout"
)

// eventCache bounds the event lookups a failing rollout costs: one
// field-selected LIST per failing pod and reason per eventCacheTTL, and
// only while the rollup has already decided the rollout fails its health
// check or can't pull its image.
type eventCacheEntry struct {
	outcome string
	at      time.Time
}

const eventCacheTTL = 20 * time.Second

var eventCache sync.Map // reason/ns/pod -> eventCacheEntry

// podEventOutcome is the newest non-empty read of the pod's events with
// the given reason.
func (s *Service) podEventOutcome(ctx context.Context, ns, pod, reason string, read func(string) string) string {
	if s.Kube == nil || s.Kube.Clientset == nil || pod == "" {
		return ""
	}
	key := reason + "/" + ns + "/" + pod
	if v, ok := eventCache.Load(key); ok {
		if e := v.(eventCacheEntry); time.Since(e.at) < eventCacheTTL {
			return e.outcome
		}
	}
	sel := fields.Set{"involvedObject.kind": "Pod", "involvedObject.name": pod, "reason": reason}.AsSelector().String()
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
			if o := read(ev.Message); o != "" && !t.Before(newest) {
				newest, outcome = t, o
			}
		}
	}
	eventCache.Store(key, eventCacheEntry{outcome: outcome, at: time.Now()})
	return outcome
}

func (s *Service) probeFailure(ctx context.Context, ns, pod string) string {
	return s.podEventOutcome(ctx, ns, pod, "Unhealthy", rollout.ProbeOutcome)
}

// pullFailure reads the registry's answer from the kubelet's "Failed to
// pull image" events: during ImagePullBackOff the pod status itself only
// says "Back-off pulling image".
func (s *Service) pullFailure(ctx context.Context, ns, pod string) string {
	return s.podEventOutcome(ctx, ns, pod, "Failed", rollout.PullCause)
}

// rolloutStatus is the wire shape of status.rollout (additive). Sent with
// state rollout_failing, and with deploying while a rollout is in flight
// (failing=false) so clients can name what is rolling out over what.
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
	if f.PullCause != "" {
		out["pullCause"] = f.PullCause
	}
	if f.NewImage != "" {
		out["newImage"] = f.NewImage
	}
	if f.PreviousImage != "" {
		out["previousImage"] = f.PreviousImage
	}
	if !f.Since.IsZero() {
		out["since"] = f.Since.UTC().Format(time.RFC3339)
	}
	if !f.PreviousSince.IsZero() {
		out["previousSince"] = f.PreviousSince.UTC().Format(time.RFC3339)
	}
	return out
}
