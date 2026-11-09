package addons

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/podreason"
)

// containerProblem renders a stuck container's reason into one line.
func containerProblem(name string, waiting *corev1.ContainerStateWaiting, terminated *corev1.ContainerStateTerminated) string {
	if waiting != nil && waiting.Reason != "" && waiting.Reason != "PodInitializing" {
		if waiting.Message != "" {
			return fmt.Sprintf("%s: %s — %s", name, waiting.Reason, waiting.Message)
		}
		return fmt.Sprintf("%s: %s", name, waiting.Reason)
	}
	if terminated != nil && terminated.ExitCode != 0 {
		if terminated.Message != "" {
			return fmt.Sprintf("%s: exited %d (%s) — %s", name, terminated.ExitCode, terminated.Reason, terminated.Message)
		}
		return fmt.Sprintf("%s: exited %d (%s)", name, terminated.ExitCode, terminated.Reason)
	}
	return ""
}

// AddonPodSummary describes one pod backing an addon.
//
// Message is the field that matters. An addon whose Helm release deploys
// cleanly but whose pods never start is otherwise silent: the CR reports
// Deployed=True, `kuso health` counts it healthy, and the only symptom is
// connection-refused at the addon's Service. Message carries the scheduler's
// or kubelet's own reason — unschedulable, a missing PriorityClass, an init
// container stuck on a missing Secret key — so the failure explains itself
// instead of requiring cluster access to diagnose.
type AddonPodSummary struct {
	Name      string `json:"name"`
	Ready     bool   `json:"ready"`
	Phase     string `json:"phase,omitempty"`
	Component string `json:"component,omitempty"`
	Restarts  int32  `json:"restarts,omitempty"`
	Message   string `json:"message,omitempty"`
	// Summary is Message in plain words (podreason), for pods that can't start.
	Summary    string   `json:"summary,omitempty"`
	Containers []string `json:"containers,omitempty"`
}

// AddonPodList is the wire shape for /api/projects/{p}/addons/{a}/pods.
type AddonPodList struct {
	Namespace string            `json:"namespace"`
	Pods      []AddonPodSummary `json:"pods"`
	// RunningImage is the datastore container's image on a running pod;
	// RunningVersion is its tag. They answer "which version is this?" when
	// spec.version is empty (chart default). Omitted when no main pod runs
	// (scaled down, CNPG-managed HA postgres) or the image is digest-only.
	RunningImage   string `json:"runningImage,omitempty"`
	RunningVersion string `json:"runningVersion,omitempty"`
}

// ListPods returns every pod belonging to an addon — the datastore itself and
// any sidecar workload the chart renders (the PgBouncer pooler, most notably).
//
// An empty list is a valid, meaningful answer: it means the chart rendered a
// workload that has no running pods, which is exactly the state that produces
// connection-refused from a Service that resolves fine.
func (s *Service) ListPods(ctx context.Context, project, addon string) (*AddonPodList, error) {
	if project == "" || addon == "" {
		return nil, fmt.Errorf("%w: project and addon are required", ErrInvalid)
	}
	cr, err := s.GetOwned(ctx, project, addon)
	if err != nil {
		return nil, err
	}
	ns := s.nsFor(ctx, project)

	pods, err := s.Kube.Clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/instance=" + cr.Name,
	})
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}

	out := &AddonPodList{Namespace: ns, Pods: make([]AddonPodSummary, 0, len(pods.Items))}
	for i := range pods.Items {
		p := &pods.Items[i]
		sum := AddonPodSummary{
			Name:      p.Name,
			Phase:     string(p.Status.Phase),
			Component: p.Labels["kuso.sislelabs.com/component"],
		}
		for _, c := range p.Status.ContainerStatuses {
			if c.Ready {
				sum.Ready = true
			}
			sum.Restarts += c.RestartCount
		}
		for _, c := range p.Spec.Containers {
			sum.Containers = append(sum.Containers, c.Name)
		}
		sum.Message = podProblem(p)
		if is := podreason.ForPod(p, time.Now()); is != nil {
			sum.Summary = is.Summary
		}
		out.Pods = append(out.Pods, sum)
		// Every addon chart puts the datastore first in containers.
		if out.RunningImage == "" && isMainAddonPod(p, cr.Name) && p.Status.Phase == corev1.PodRunning && len(p.Spec.Containers) > 0 {
			out.RunningImage = p.Spec.Containers[0].Image
			out.RunningVersion = imageVersion(out.RunningImage)
		}
	}
	return out, nil
}

// isMainAddonPod reports whether the pod belongs to the addon's own
// workload — the StatefulSet (or Deployment) named after the release —
// rather than a sibling workload such as the pooler or redis sentinels,
// which share the instance label.
func isMainAddonPod(p *corev1.Pod, crName string) bool {
	owner := metav1.GetControllerOf(p)
	if owner == nil {
		return false
	}
	switch owner.Kind {
	case "StatefulSet":
		return owner.Name == crName
	case "ReplicaSet":
		i := strings.LastIndexByte(owner.Name, '-')
		return i > 0 && owner.Name[:i] == crName
	}
	return false
}

// imageVersion returns the tag of an image reference: "postgres:16-alpine"
// → "16-alpine", "reg:5000/redis:7@sha256:…" → "7". A reference without a
// tag is "latest" unless it is pinned by digest alone, which has no
// human-readable version ("").
func imageVersion(image string) string {
	name, digest, _ := strings.Cut(image, "@")
	// The tag colon is the one after the last path segment; an earlier
	// colon is a registry port.
	if i := strings.LastIndexByte(name, ':'); i > strings.LastIndexByte(name, '/') {
		return name[i+1:]
	}
	if digest != "" || name == "" {
		return ""
	}
	return "latest"
}

// podProblem extracts the most specific explanation available for a pod that
// isn't serving. Checked in order of usefulness: a blocked init container beats
// a blocked app container, which beats an unscheduled pod, which beats the
// pod-level Reason. Returns "" for a healthy running pod.
func podProblem(p *corev1.Pod) string {
	// Init containers first — they gate everything after them, so when one is
	// stuck the app container's "PodInitializing" is just noise.
	for _, c := range p.Status.InitContainerStatuses {
		if msg := containerProblem(c.Name, c.State.Waiting, c.State.Terminated); msg != "" {
			return msg
		}
	}
	for _, c := range p.Status.ContainerStatuses {
		if c.Ready {
			continue
		}
		if msg := containerProblem(c.Name, c.State.Waiting, c.State.Terminated); msg != "" {
			return msg
		}
	}
	// Not scheduled at all: the scheduler's message names the reason (no node
	// matched, a PriorityClass is missing, resources are insufficient).
	for _, cond := range p.Status.Conditions {
		if cond.Type == "PodScheduled" && cond.Status == "False" {
			if cond.Message != "" {
				return strings.TrimSpace(cond.Reason + ": " + cond.Message)
			}
			return cond.Reason
		}
	}
	if p.Status.Reason != "" && p.Status.Phase != "Running" {
		return p.Status.Reason
	}
	return ""
}
