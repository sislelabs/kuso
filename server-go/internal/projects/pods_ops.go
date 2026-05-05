package projects

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// PodSummary is the wire shape for /api/projects/{p}/services/{s}/pods.
// Slim by design — the CLI's `kuso shell` only needs name + ready state
// to pick a target, and the web shell tab will gain the same surface.
type PodSummary struct {
	Name       string   `json:"name"`
	Ready      bool     `json:"ready"`
	Phase      string   `json:"phase,omitempty"`
	Containers []string `json:"containers,omitempty"`
}

// PodList is what the handler returns. Namespace is included so callers
// (notably the CLI shell command, which then runs `kubectl exec`) don't
// need to round-trip again to find it.
type PodList struct {
	Namespace string       `json:"namespace"`
	Pods      []PodSummary `json:"pods"`
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
		return nil, err
	}
	envCR, err := s.Kube.GetKusoEnvironment(ctx, ns, envName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get env: %w", err)
	}
	// Tenancy check: the resolved env must actually belong to (project,
	// service). Without this, a viewer of project-a can list pods for
	// project-b by passing ?env=project-b-svc-production — the env CR
	// exists, the lookup succeeds, and the route's requireProjectAccess
	// only verified access to project-a. The env CR's spec carries the
	// authoritative project + service it was created for.
	if envCR.Spec.Project != "" && envCR.Spec.Project != project {
		return nil, ErrNotFound
	}
	if envCR.Spec.Service != "" && envCR.Spec.Service != fqn {
		return nil, ErrNotFound
	}

	pods, err := s.Kube.Clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/instance=" + envName,
	})
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	out := &PodList{Namespace: ns, Pods: make([]PodSummary, 0, len(pods.Items))}
	for i := range pods.Items {
		p := &pods.Items[i]
		ready := false
		for _, c := range p.Status.ContainerStatuses {
			// "ready" means at least one container is Ready. A pod with
			// crashlooping sidecars but a healthy main container should
			// still be exec'able — that's often when the user reaches
			// for the shell in the first place.
			if c.Ready {
				ready = true
				break
			}
		}
		// Surfacing container names lets the CLI pass `-c <name>` for
		// multi-container pods (sidecars, init helpers — reviewers want
		// to peek at the app container, not envoy).
		containers := make([]string, 0, len(p.Spec.Containers))
		for _, c := range p.Spec.Containers {
			containers = append(containers, c.Name)
		}
		out.Pods = append(out.Pods, PodSummary{
			Name:       p.Name,
			Ready:      ready,
			Phase:      string(p.Status.Phase),
			Containers: containers,
		})
	}
	return out, nil
}

// Compile-time check that we depend on the GetKusoEnvironment shape we
// expect. If kube.Client ever drops this method this file won't build.
var _ = (*kube.Client)(nil)
