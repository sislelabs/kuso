// Package podreason turns the scheduler's and kubelet's raw reasons for a
// pod that isn't running ("0/1 nodes are available: 1 node(s) had
// untolerated taint {node.kubernetes.io/disk-pressure: }") into one plain
// sentence, keeping the raw text as detail. The service rollup, the addon
// rollup and the pod/event endpoints all use it, so every surface explains
// a stuck pod the same way.
package podreason

import (
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Kind values cross the wire (status.stuckReason); never rename them.
const (
	KindUnschedulable = "unschedulable"
	KindImagePull     = "image_pull"
	KindConfig        = "config_error"
	KindCrashLoop     = "crashloop"
	KindRolloutStall  = "rollout_stalled"
)

// StallAfter is how long a pod may stay not-ready, with no more specific
// reason, before its rollout counts as stuck.
const StallAfter = 10 * time.Minute

// Issue is why one pod isn't serving.
type Issue struct {
	Kind string
	// Summary is the human sentence; Message is the raw scheduler/kubelet text.
	Summary string
	Message string
	Since   time.Time
}

// Summarize maps a waiting/condition reason plus its raw message to a
// plain sentence. Unknown reasons fall back to the raw text.
func Summarize(reason, message string) string {
	switch reason {
	case "Unschedulable", "SchedulerError":
		return SummarizeScheduling(message)
	case "ImagePullBackOff", "ErrImagePull":
		m := strings.ToLower(message)
		switch {
		case strings.Contains(m, "not found") || strings.Contains(m, "manifest unknown"):
			return "The image tag doesn't exist in the registry"
		case strings.Contains(m, "unauthorized") || strings.Contains(m, "denied") || strings.Contains(m, "authentication"):
			return "The registry refused to hand over the image (check registry credentials)"
		}
		return "The image can't be pulled"
	case "InvalidImageName":
		return "The image name is invalid"
	case "CreateContainerConfigError":
		if strings.Contains(message, "not found") {
			return "A secret or config value the container needs is missing"
		}
		return "The container's configuration is invalid"
	case "CreateContainerError", "RunContainerError":
		return "The container couldn't be started"
	case "CrashLoopBackOff":
		return "The app keeps crashing on start"
	case "OOMKilled":
		return "The app ran out of memory and was killed"
	case "ContainerCreating", "PodInitializing":
		return "The container is still being created"
	}
	if message != "" {
		return message
	}
	return reason
}

// schedulingCauses is checked in order; the first hit names the cause.
var schedulingCauses = []struct{ needle, text string }{
	{"node.kubernetes.io/disk-pressure", "The node is low on disk, so the pod can't be scheduled"},
	{"node.kubernetes.io/memory-pressure", "The node is low on memory, so the pod can't be scheduled"},
	{"node.kubernetes.io/pid-pressure", "The node is running too many processes, so the pod can't be scheduled"},
	{"node.kubernetes.io/not-ready", "The node isn't ready, so the pod can't be scheduled"},
	{"node.kubernetes.io/unreachable", "The node is unreachable, so the pod can't be scheduled"},
	{"node.kubernetes.io/unschedulable", "The node is cordoned, so the pod can't be scheduled"},
	{"insufficient cpu", "No node has enough free CPU for the pod's request"},
	{"insufficient memory", "No node has enough free memory for the pod's request"},
	{"insufficient ephemeral-storage", "No node has enough free disk for the pod's request"},
	{"too many pods", "Every node is at its pod limit"},
	{"didn't match pod's node affinity/selector", "No node matches the placement labels"},
	{"node affinity/selector", "No node matches the placement labels"},
	{"volume node affinity conflict", "The pod's volume lives on a node it can't run on"},
	{"unbound immediate persistentvolumeclaims", "The pod's storage volume isn't ready yet"},
	{"persistentvolumeclaim", "The pod's storage volume isn't ready yet"},
	{"didn't have free ports", "The port the pod needs is already taken on every node"},
	{"untolerated taint", "No node accepts the pod (every node is tainted)"},
}

// SummarizeScheduling explains a PodScheduled=False message.
func SummarizeScheduling(message string) string {
	m := strings.ToLower(message)
	for _, c := range schedulingCauses {
		if strings.Contains(m, c.needle) {
			return c.text
		}
	}
	return "No node can run the pod right now"
}

// ForPod returns why the pod isn't serving, or nil when it's ready,
// finished, terminating, or simply still starting inside the stall
// window. Init containers gate the app containers, so they're read first.
func ForPod(p *corev1.Pod, now time.Time) *Issue {
	if p == nil || p.DeletionTimestamp != nil || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
		return nil
	}
	for _, list := range [][]corev1.ContainerStatus{p.Status.InitContainerStatuses, p.Status.ContainerStatuses} {
		for i := range list {
			w := list[i].State.Waiting
			if w == nil {
				continue
			}
			kind := ""
			switch w.Reason {
			case "ImagePullBackOff", "ErrImagePull", "InvalidImageName":
				kind = KindImagePull
			case "CreateContainerConfigError", "CreateContainerError", "RunContainerError":
				kind = KindConfig
			case "CrashLoopBackOff":
				kind = KindCrashLoop
			}
			if kind != "" {
				return &Issue{Kind: kind, Summary: Summarize(w.Reason, w.Message), Message: rawOf(w.Reason, w.Message), Since: sinceOf(p)}
			}
		}
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Reason == "Unschedulable" {
			return &Issue{Kind: KindUnschedulable, Summary: SummarizeScheduling(c.Message), Message: rawOf(c.Reason, c.Message), Since: timeOr(c.LastTransitionTime, p.CreationTimestamp)}
		}
	}
	if podReady(p) {
		return nil
	}
	since := p.CreationTimestamp.Time
	if since.IsZero() || now.Sub(since) < StallAfter {
		return nil
	}
	return &Issue{
		Kind:    KindRolloutStall,
		Summary: fmt.Sprintf("The new instance hasn't become ready in %s", roundDur(now.Sub(since))),
		Message: waitingText(p),
		Since:   since,
	}
}

func podReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func waitingText(p *corev1.Pod) string {
	for _, cs := range p.Status.ContainerStatuses {
		if w := cs.State.Waiting; w != nil && w.Reason != "" {
			return rawOf(w.Reason, w.Message)
		}
	}
	return "pod " + string(p.Status.Phase) + ", not ready"
}

func rawOf(reason, message string) string {
	if message == "" {
		return reason
	}
	return reason + ": " + message
}

func sinceOf(p *corev1.Pod) time.Time { return p.CreationTimestamp.Time }

func timeOr(t, fallback metav1.Time) time.Time {
	if !t.IsZero() {
		return t.Time
	}
	return fallback.Time
}

func roundDur(d time.Duration) string {
	if d >= time.Hour {
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%d min", int(d.Minutes()))
}

// Worst picks the issue to report for a set of pods: a hard cause
// (unschedulable, image, config) beats a crashloop, which beats a stall.
// Pods owned by a Job (release hooks share the instance label) are skipped.
func Worst(pods []*corev1.Pod, now time.Time) *Issue {
	var best *Issue
	rank := func(k string) int {
		switch k {
		case KindUnschedulable, KindImagePull, KindConfig:
			return 0
		case KindCrashLoop:
			return 1
		}
		return 2
	}
	for _, p := range pods {
		if owner := metav1.GetControllerOf(p); owner != nil && owner.Kind == "Job" {
			continue
		}
		is := ForPod(p, now)
		if is == nil {
			continue
		}
		if best == nil || rank(is.Kind) < rank(best.Kind) {
			best = is
		}
	}
	return best
}
