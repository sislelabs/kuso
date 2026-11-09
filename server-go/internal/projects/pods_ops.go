package projects

import (
	"context"
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
	"kuso/server/internal/podreason"
)

// PodSummary is the wire shape for /api/projects/{p}/services/{s}/pods.
// Slim by design — the CLI's `kuso shell` only needs name + ready state
// to pick a target, and the web shell tab will gain the same surface.
type PodSummary struct {
	Name       string   `json:"name"`
	Ready      bool     `json:"ready"`
	Phase      string   `json:"phase,omitempty"`
	Containers []string `json:"containers,omitempty"`
	// Restarts sums RestartCount across the pod's containers.
	Restarts int32 `json:"restarts"`
	// Reason/Message explain a pod that isn't ready yet: the first
	// waiting container's reason (ContainerCreating, ImagePullBackOff,
	// CrashLoopBackOff…), else the Pending pod's unschedulable condition.
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
	// Summary is Reason/Message in plain words (podreason.Summarize).
	Summary string `json:"summary,omitempty"`
	// Current is false for pods of an older ReplicaSet still draining
	// mid-rollout and for release-hook Job pods; ready/desired counts
	// should use only current pods. Absent on older servers: treat
	// missing as current.
	Current   bool   `json:"current"`
	CreatedAt string `json:"createdAt,omitempty"`
	// ImageID is the first container's resolved image (registry@sha256:…)
	// as the kubelet reports it; empty until the image is pulled.
	ImageID string `json:"imageID,omitempty"`
	// Image is the first container's spec image reference (repo:tag), so
	// a client can tell which release a pod runs before trusting ImageID.
	Image string `json:"image,omitempty"`
}

// PodList is what the handler returns. Namespace is included so callers
// (notably the CLI shell command, which then runs `kubectl exec`) don't
// need to round-trip again to find it.
type PodList struct {
	Namespace string       `json:"namespace"`
	Pods      []PodSummary `json:"pods"`
}

// resolveOwnedEnv resolves (project, service, env) to the env CR and its
// namespace, and verifies the env CR really belongs to that project +
// service.
func (s *Service) resolveOwnedEnv(ctx context.Context, project, service, env string) (string, *kube.KusoEnvironment, error) {
	if env == "" {
		env = "production"
	}
	fqn := service
	if !strings.HasPrefix(service, project+"-") {
		fqn = project + "-" + service
	}
	envName := env
	if !strings.Contains(env, "-") {
		envName = fqn + "-" + env
	}
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return "", nil, err
	}
	envCR, err := s.Kube.GetKusoEnvironment(ctx, ns, envName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil, ErrNotFound
		}
		return "", nil, fmt.Errorf("get env: %w", err)
	}
	// Tenancy check: the resolved env must actually belong to (project,
	// service). Without this, a viewer of project-a can list pods for
	// project-b by passing ?env=project-b-svc-production — the env CR
	// exists, the lookup succeeds, and the route's requireProjectAccess
	// only verified access to project-a. The env CR's spec carries the
	// authoritative project + service it was created for.
	// Strict form (as logs.Tail): a `!= ""` guard let an env CR with an
	// empty spec.project/service pass for any project.
	if envCR.Spec.Project != project || envCR.Spec.Service != fqn {
		return "", nil, ErrNotFound
	}
	return ns, envCR, nil
}

// ListPods returns the pods backing a service's environment. env defaults
// to "production" and accepts either the short form ("production",
// "preview-pr-7") or the fully-qualified env CR name. Pod selection
// mirrors logs.Tail: app.kubernetes.io/instance=<envName>.
//
// We require the env CR to exist before listing — otherwise a typo'd env
// name would silently return zero pods, which is the same response as a
// scaled-to-zero service, and the caller can't tell the difference.
func (s *Service) ListPods(ctx context.Context, project, service, env string) (*PodList, error) {
	ns, envCR, err := s.resolveOwnedEnv(ctx, project, service, env)
	if err != nil {
		return nil, err
	}
	envName := envCR.Name

	pods, err := s.Kube.Clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/instance=" + envName,
	})
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	currentRS := s.currentReplicaSet(ctx, ns, envName)
	out := &PodList{Namespace: ns, Pods: make([]PodSummary, 0, len(pods.Items))}
	for i := range pods.Items {
		p := &pods.Items[i]
		ready := false
		var restarts int32
		imageID := ""
		for _, c := range p.Status.ContainerStatuses {
			if imageID == "" {
				imageID = c.ImageID
			}
			// "ready" means at least one container is Ready. A pod with
			// crashlooping sidecars but a healthy main container should
			// still be exec'able — that's often when the user reaches
			// for the shell in the first place.
			if c.Ready {
				ready = true
			}
			restarts += c.RestartCount
		}
		// Surfacing container names lets the CLI pass `-c <name>` for
		// multi-container pods (sidecars, init helpers — reviewers want
		// to peek at the app container, not envoy).
		containers := make([]string, 0, len(p.Spec.Containers))
		for _, c := range p.Spec.Containers {
			containers = append(containers, c.Name)
		}
		reason, message := podWaitReason(p)
		sum := PodSummary{
			Name:       p.Name,
			Ready:      ready,
			Phase:      string(p.Status.Phase),
			Containers: containers,
			Restarts:   restarts,
			Reason:     reason,
			Message:    message,
			Current:    podIsCurrent(p, currentRS),
			CreatedAt:  p.CreationTimestamp.UTC().Format(time.RFC3339),
			ImageID:    imageID,
		}
		if len(p.Spec.Containers) > 0 {
			sum.Image = p.Spec.Containers[0].Image
		}
		if reason != "" {
			sum.Summary = podreason.Summarize(reason, message)
		}
		out.Pods = append(out.Pods, sum)
	}
	return out, nil
}

// currentReplicaSet names the env Deployment's newest ReplicaSet (the one
// whose revision matches the Deployment's). "" when it can't be told, in
// which case every live pod counts as current.
func (s *Service) currentReplicaSet(ctx context.Context, ns, envName string) string {
	const revAnno = "deployment.kubernetes.io/revision"
	var dep *appsv1.Deployment
	if s.Kube.Cache != nil {
		if d, ok := s.Kube.Cache.GetDeployment(ns, envName); ok {
			dep = d
		}
	}
	if dep == nil {
		d, err := s.Kube.Clientset.AppsV1().Deployments(ns).Get(ctx, envName, metav1.GetOptions{})
		if err != nil {
			return ""
		}
		dep = d
	}
	rev := dep.Annotations[revAnno]
	if rev == "" {
		return ""
	}
	rsList, err := s.Kube.Clientset.AppsV1().ReplicaSets(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/instance=" + envName,
	})
	if err != nil {
		return ""
	}
	for i := range rsList.Items {
		rs := &rsList.Items[i]
		if owner := metav1.GetControllerOf(rs); owner != nil && owner.UID == dep.UID && rs.Annotations[revAnno] == rev {
			return rs.Name
		}
	}
	return ""
}

// podIsCurrent: a live pod of the current ReplicaSet. Finished pods and
// Job (release hook) pods never are.
func podIsCurrent(p *corev1.Pod, currentRS string) bool {
	if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed || p.DeletionTimestamp != nil {
		return false
	}
	owner := metav1.GetControllerOf(p)
	if owner == nil || owner.Kind != "ReplicaSet" {
		return false
	}
	return currentRS == "" || owner.Name == currentRS
}

// podWaitReason reports why a pod isn't running yet: a waiting init or
// app container's reason, else a PodScheduled=False condition.
func podWaitReason(p *corev1.Pod) (string, string) {
	for _, list := range [][]corev1.ContainerStatus{p.Status.InitContainerStatuses, p.Status.ContainerStatuses} {
		for _, c := range list {
			if w := c.State.Waiting; w != nil && w.Reason != "" {
				return w.Reason, w.Message
			}
		}
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse {
			return c.Reason, c.Message
		}
	}
	return "", ""
}

// Compile-time check that we depend on the GetKusoEnvironment shape we
// expect. If kube.Client ever drops this method this file won't build.
var _ = (*kube.Client)(nil)
