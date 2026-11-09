package builds

import (
	"context"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
	"kuso/server/internal/podreason"
)

// BuildStallAfter is how long an in-flight build may go without its pod
// running before the UI is told why. Cloning starts within seconds once
// the pod is scheduled and the builder image is present.
const BuildStallAfter = 60 * time.Second

// Stall kinds cross the wire (buildSummary.stalled.kind).
const (
	StallImagePull     = "image_pull"
	StallUnschedulable = "unschedulable"
	StallConfig        = "config_error"
	StallNoPod         = "no_pod"
	StallStarting      = "starting"
)

// Stall says why an in-flight build isn't making progress.
type Stall struct {
	Kind    string    `json:"kind"`
	Summary string    `json:"summary"`
	Detail  string    `json:"detail,omitempty"`
	Since   time.Time `json:"since"`
}

// StallFor explains a build whose pod hasn't started running BuildStallAfter
// after the build began, or returns nil when it's progressing (or too
// early to say). pods are the build Job's pods.
func StallFor(pods []corev1.Pod, started, now time.Time) *Stall {
	if started.IsZero() || now.Sub(started) < BuildStallAfter {
		return nil
	}
	if len(pods) == 0 {
		return &Stall{Kind: StallNoPod, Summary: "The build hasn't started: no build pod was created", Since: started}
	}
	p := &pods[0]
	for i := 1; i < len(pods); i++ {
		if pods[i].CreationTimestamp.After(p.CreationTimestamp.Time) {
			p = &pods[i]
		}
	}
	if p.Status.Phase != corev1.PodPending {
		return nil
	}
	since := p.CreationTimestamp.Time
	if since.IsZero() {
		since = started
	}
	for _, list := range [][]corev1.ContainerStatus{p.Status.InitContainerStatuses, p.Status.ContainerStatuses} {
		for i := range list {
			// The clone step is an init container: the pod stays Pending
			// while it runs, which is progress.
			if list[i].State.Running != nil {
				return nil
			}
			w := list[i].State.Waiting
			if w == nil {
				continue
			}
			raw := w.Reason
			if w.Message != "" {
				raw += ": " + w.Message
			}
			switch w.Reason {
			case "ImagePullBackOff", "ErrImagePull", "InvalidImageName":
				return &Stall{Kind: StallImagePull, Summary: "Builder image can't be pulled", Detail: raw, Since: since}
			case "CreateContainerConfigError", "CreateContainerError", "RunContainerError":
				return &Stall{Kind: StallConfig, Summary: "The build pod can't start: " + lowerFirst(podreason.Summarize(w.Reason, w.Message)), Detail: raw, Since: since}
			}
		}
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse {
			return &Stall{
				Kind:    StallUnschedulable,
				Summary: "Waiting for a node: " + lowerFirst(podreason.SummarizeScheduling(c.Message)),
				Detail:  strings.TrimSpace(c.Reason + ": " + c.Message),
				Since:   since,
			}
		}
	}
	return &Stall{Kind: StallStarting, Summary: "The build pod is still starting (downloading the builder image or waiting on storage)", Detail: "pod Pending", Since: since}
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// Stalls checks every in-flight build in bs and returns the stalled ones
// by name. Only builds past BuildStallAfter cost a pod list.
func (s *Service) Stalls(ctx context.Context, bs []kube.KusoBuild, now time.Time) map[string]*Stall {
	out := map[string]*Stall{}
	if s == nil || s.Kube == nil || s.Kube.Clientset == nil {
		return out
	}
	for i := range bs {
		b := &bs[i]
		phase := b.Annotations[annPhase]
		if phase != "pending" && phase != "running" {
			continue
		}
		// pending = Job dispatched; running = the Job has a pod, which
		// may still be unscheduled or unable to pull the builder image.
		started := b.CreationTimestamp.Time
		for _, ann := range []string{annDispatchedAt, annStartedAt} {
			if t, err := time.Parse(time.RFC3339, b.Annotations[ann]); err == nil {
				started = t
			}
		}
		if started.IsZero() || now.Sub(started) < BuildStallAfter {
			continue
		}
		lctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		pods, err := s.Kube.Clientset.CoreV1().Pods(b.Namespace).List(lctx, metav1.ListOptions{
			LabelSelector: kube.LabelSelector(map[string]string{"app.kubernetes.io/instance": b.Name}),
		})
		cancel()
		if err != nil {
			continue
		}
		if st := StallFor(pods.Items, started, now); st != nil {
			out[b.Name] = st
		}
	}
	return out
}
