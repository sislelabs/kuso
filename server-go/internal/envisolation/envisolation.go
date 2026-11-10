// Package envisolation gates the project NetworkPolicies that stop
// preview / env-group / named-env pods (and build pods) from reaching
// production pods and unsubscribed addons (review ISO-3).
//
// The policies key on the kuso.sislelabs.com/env pod label, which the
// kusoenvironment chart renders from spec.envScope. A pod created before
// the env CR was stamped carries no label and is treated as legacy
// (allowed everywhere it was before), so the deny can never cut a pod off
// from something it reached before it rolled. The remaining hazard is a
// labelled pod that depends on an addon it does not subscribe to, which is
// what enabling isolation is meant to refuse; Readiness exists so an
// operator flips the switch only once every env pod carries its labels.
package envisolation

import (
	"context"
	"errors"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

var (
	ErrNotReady = errors.New("env isolation: not ready")
	ErrNotFound = errors.New("env isolation: project not found")
)

// Report is what GET /api/projects/{p}/env-isolation returns.
type Report struct {
	Project string `json:"project"`
	Enabled bool   `json:"enabled"`
	// Ready: every env CR carries an env label mirrored into spec.envScope
	// and every env pod carries the label.
	Ready bool `json:"ready"`
	// UnstampedEnvs have the label but no spec.envScope yet. Stamp fixes
	// them (and rolls their pods).
	UnstampedEnvs []string `json:"unstampedEnvs"`
	// UnlabelledEnvs have no env label at all and are not a plain
	// production env Stamp can label; they need a manual label.
	UnlabelledEnvs []string `json:"unlabelledEnvs"`
	// UnlabelledPods are env pods still running from an old template.
	UnlabelledPods []string `json:"unlabelledPods"`
}

type Service struct {
	Kube   *kube.Client
	HomeNS string
	NS     interface {
		NamespaceFor(ctx context.Context, project string) string
	}
}

func (s *Service) project(ctx context.Context, project string) (*kube.KusoProject, error) {
	p, err := s.Kube.GetKusoProject(ctx, s.HomeNS, project)
	if err != nil || p == nil {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, project)
	}
	return p, nil
}

func (s *Service) envs(ctx context.Context, project string) (string, []kube.KusoEnvironment, error) {
	ns := s.NS.NamespaceFor(ctx, project)
	envs, err := s.Kube.ListKusoEnvironmentsByLabels(ctx, ns, map[string]string{kube.LabelProject: project})
	if err != nil {
		return ns, nil, fmt.Errorf("list envs: %w", err)
	}
	return ns, envs, nil
}

// Readiness reports whether envIsolation can be switched on safely.
func (s *Service) Readiness(ctx context.Context, project string) (*Report, error) {
	p, err := s.project(ctx, project)
	if err != nil {
		return nil, err
	}
	ns, envs, err := s.envs(ctx, project)
	if err != nil {
		return nil, err
	}
	rep := &Report{
		Project:        project,
		Enabled:        enabled(p),
		UnstampedEnvs:  []string{},
		UnlabelledEnvs: []string{},
		UnlabelledPods: []string{},
	}
	for _, e := range envs {
		scope := e.Labels[kube.LabelEnv]
		switch {
		case scope == "":
			rep.UnlabelledEnvs = append(rep.UnlabelledEnvs, e.Name)
		case e.Spec.EnvScope != scope:
			rep.UnstampedEnvs = append(rep.UnstampedEnvs, e.Name)
		}
	}
	pods, err := s.Kube.Clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: kube.LabelSelector(map[string]string{
			kube.LabelProject:        project,
			"app.kubernetes.io/name": "kusoenvironment",
		}),
	})
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		if pod.Labels[kube.LabelEnv] == "" {
			rep.UnlabelledPods = append(rep.UnlabelledPods, pod.Name)
		}
	}
	sort.Strings(rep.UnstampedEnvs)
	sort.Strings(rep.UnlabelledEnvs)
	sort.Strings(rep.UnlabelledPods)
	rep.Ready = len(rep.UnstampedEnvs) == 0 && len(rep.UnlabelledEnvs) == 0 && len(rep.UnlabelledPods) == 0
	return rep, nil
}

// Stamp writes spec.envScope on every env CR that lacks it, labelling a
// plain production env (kind production, owned by a service that is not
// an env-group copy) as env=production first. Each write rolls that env's
// pods once, through the Deployment's normal rolling update.
func (s *Service) Stamp(ctx context.Context, project string) ([]string, error) {
	if _, err := s.project(ctx, project); err != nil {
		return nil, err
	}
	ns, envs, err := s.envs(ctx, project)
	if err != nil {
		return nil, err
	}
	var stamped []string
	for i := range envs {
		e := envs[i]
		scope := e.Labels[kube.LabelEnv]
		if scope == "" {
			if !s.plainProduction(ctx, ns, &e) {
				continue
			}
			scope = "production"
		}
		if e.Spec.EnvScope == scope && e.Labels[kube.LabelEnv] == scope {
			continue
		}
		if err := s.Kube.StampKusoEnvironmentScope(ctx, ns, e.Name, scope); err != nil {
			return stamped, fmt.Errorf("stamp %s: %w", e.Name, err)
		}
		stamped = append(stamped, e.Name)
	}
	return stamped, nil
}

func (s *Service) plainProduction(ctx context.Context, ns string, e *kube.KusoEnvironment) bool {
	if e.Spec.Kind != "" && e.Spec.Kind != "production" {
		return false
	}
	svc, err := s.Kube.GetKusoService(ctx, ns, e.Spec.Service)
	if err != nil || svc == nil {
		return false
	}
	return svc.Labels[kube.LabelEnv] == ""
}

// SetEnabled flips spec.networkPolicy.envIsolation. Enabling refuses
// (ErrNotReady) unless Readiness passes; disabling always works.
func (s *Service) SetEnabled(ctx context.Context, project string, on bool) (*Report, error) {
	if on {
		rep, err := s.Readiness(ctx, project)
		if err != nil {
			return nil, err
		}
		if !rep.Ready {
			return rep, fmt.Errorf("%w: %d unstamped envs, %d unlabelled envs, %d unlabelled pods",
				ErrNotReady, len(rep.UnstampedEnvs), len(rep.UnlabelledEnvs), len(rep.UnlabelledPods))
		}
	}
	_, err := s.Kube.UpdateKusoProjectWithRetry(ctx, s.HomeNS, project, func(p *kube.KusoProject) error {
		if p.Spec.NetworkPolicy == nil {
			p.Spec.NetworkPolicy = &kube.KusoProjectNetworkPolicy{}
		}
		v := on
		p.Spec.NetworkPolicy.EnvIsolation = &v
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("update project: %w", err)
	}
	return s.Readiness(ctx, project)
}

func enabled(p *kube.KusoProject) bool {
	return p.Spec.NetworkPolicy != nil && p.Spec.NetworkPolicy.EnvIsolation != nil && *p.Spec.NetworkPolicy.EnvIsolation
}
