package addons

import (
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"

	"kuso/server/internal/kube"
	"kuso/server/internal/podreason"
)

// Addon rollup states (status.state on the addon list). Additive wire
// contract, spellings frozen:
//
//	status.state       live | provisioning | degraded | failing | stuck | external
//	status.stateDetail human reason
//	status.pods        {ready, total} over the addon's live pods
//	status.stuck       {reason, summary, message, since} when state=stuck
//
// The canvas, Health page and addon overview read these so a Deployed
// helm release with an unschedulable pod is never "Live" in one place
// and "Failing" in another.
const (
	addonStateLive         = "live"
	addonStateProvisioning = "provisioning"
	addonStateDegraded     = "degraded"
	addonStateFailing      = "failing"
	addonStateStuck        = "stuck"
	addonStateExternal     = "external"
)

type addonStateInput struct {
	External bool
	// Failed: ReleaseFailed/Irreconcilable=True; FailMessage is its text.
	Failed      bool
	FailMessage string
	Deployed    bool
	Ready       int
	Total       int
	// Young: every live pod is younger than podreason.StallAfter.
	Young bool
	Issue *podreason.Issue
}

func deriveAddonState(in addonStateInput) (string, string) {
	switch {
	case in.External:
		return addonStateExternal, "Managed outside kuso"
	case in.Failed:
		if in.FailMessage != "" {
			return addonStateFailing, in.FailMessage
		}
		return addonStateFailing, "Helm release failed"
	case in.Total == 0 && in.Deployed:
		return addonStateFailing, "No pods running"
	case in.Total == 0:
		return addonStateProvisioning, "Waiting for the first pod"
	case in.Ready == in.Total:
		return addonStateLive, fmt.Sprintf("%d/%d pods ready", in.Ready, in.Total)
	case in.Issue != nil && in.Issue.Kind != podreason.KindCrashLoop:
		if in.Ready > 0 {
			return addonStateStuck, fmt.Sprintf("%s; %d/%d pods ready", in.Issue.Summary, in.Ready, in.Total)
		}
		return addonStateStuck, in.Issue.Summary
	case in.Ready == 0 && in.Young:
		return addonStateProvisioning, fmt.Sprintf("0/%d pods ready, starting", in.Total)
	case in.Ready == 0:
		if in.Issue != nil {
			return addonStateFailing, in.Issue.Summary
		}
		return addonStateFailing, fmt.Sprintf("0/%d pods ready", in.Total)
	}
	return addonStateDegraded, fmt.Sprintf("%d/%d pods ready", in.Ready, in.Total)
}

// WithLiveStatus stamps the rollup onto copies of the addons' status maps
// from the shared pod informer (no apiserver calls). A cold cache leaves
// the addons untouched and clients fall back to the helm conditions.
// Addons on a shared instance server have no pods of their own and are
// left alone too.
func (s *Service) WithLiveStatus(list []kube.KusoAddon) []kube.KusoAddon {
	if s.Kube == nil || s.Kube.Cache == nil {
		return list
	}
	now := time.Now()
	for i := range list {
		a := &list[i]
		external := a.Spec.External != nil && (a.Spec.Pooler == nil || !a.Spec.Pooler.Enabled)
		if a.Spec.UseInstanceAddon != "" && !external {
			continue
		}
		in := addonStateInput{External: external}
		in.Failed, in.FailMessage, in.Deployed = helmConditions(a.Status)
		if !external {
			pods, ok := s.Kube.Cache.ListPodsByLabel(labels.SelectorFromSet(labels.Set{"app.kubernetes.io/instance": a.Name}))
			if !ok {
				continue
			}
			live := make([]*corev1.Pod, 0, len(pods))
			in.Young = true
			for _, p := range pods {
				if p.Namespace != a.Namespace || p.DeletionTimestamp != nil ||
					p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
					continue
				}
				live = append(live, p)
				if podReady(p) {
					in.Ready++
				}
				if now.Sub(p.CreationTimestamp.Time) >= podreason.StallAfter {
					in.Young = false
				}
			}
			in.Total = len(live)
			in.Issue = podreason.Worst(live, now)
		}
		state, detail := deriveAddonState(in)
		st := make(map[string]any, len(a.Status)+4)
		for k, v := range a.Status {
			st[k] = v
		}
		st["state"] = state
		st["stateDetail"] = detail
		if !external {
			st["pods"] = map[string]any{"ready": in.Ready, "total": in.Total}
		}
		if state == addonStateStuck && in.Issue != nil {
			stuck := map[string]any{"reason": in.Issue.Kind, "summary": in.Issue.Summary, "message": in.Issue.Message}
			if !in.Issue.Since.IsZero() {
				stuck["since"] = in.Issue.Since.UTC().Format(time.RFC3339)
			}
			st["stuck"] = stuck
		}
		a.Status = st
	}
	return list
}

func podReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// helmConditions reads the helm-operator conditions off an addon status.
func helmConditions(status map[string]any) (failed bool, message string, deployed bool) {
	conds, _ := status["conditions"].([]any)
	for _, raw := range conds {
		c, ok := raw.(map[string]any)
		if !ok || c["status"] != "True" {
			continue
		}
		switch c["type"] {
		case "ReleaseFailed", "Irreconcilable":
			failed = true
			if m, ok := c["message"].(string); ok && message == "" {
				message = m
			}
		case "Deployed":
			deployed = true
		}
	}
	return failed, message, deployed
}
