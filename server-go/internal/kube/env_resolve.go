package kube

import (
	"context"
	"errors"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// ErrEnvNotFound is returned by ResolveServiceEnv when no env of
// project/service matches the selector. Callers map it to their own
// not-found sentinel.
var ErrEnvNotFound = errors.New("kube: environment not found")

// ResolveServiceEnv finds the env CR that (project, service, env) addresses.
// env is what the UI and CLI send: an env-group label ("production",
// "staging", "preview-pr-7") or a CR name. Empty means production.
//
// Env-groups clone each service into KusoService "<fqn>-<group>" whose only
// env is "<fqn>-<group>-production" (label env=<group>), so the group's env
// of a base service lives on a different service. Order:
//
//  1. an env of this service labelled env=<env>;
//  2. for a non-production group, the env labelled env=<env> on the group's
//     clone "<fqn>-<env>" — so `logs web --env staging` reaches web-staging;
//  3. CR-name forms (<env>, <fqn>-<env>, <fqn>-pr-N) owned by this service —
//     unlabelled legacy CRs and `web-staging --env production`.
//
// Every candidate is checked against spec.project and spec.service: the
// caller is authorized for project only, and a label collision must not
// leak another project's env.
func (c *Client) ResolveServiceEnv(ctx context.Context, ns, project, service, env string) (*KusoEnvironment, error) {
	if env == "" {
		env = "production"
	}
	fqn := service
	if !strings.HasPrefix(service, project+"-") {
		fqn = project + "-" + service
	}

	labelled, err := c.ListKusoEnvironmentsByLabels(ctx, ns, map[string]string{
		LabelProject: project,
		LabelEnv:     env,
	})
	if err != nil {
		return nil, fmt.Errorf("list envs of %s: %w", project, err)
	}
	owners := []string{fqn}
	if env != "production" {
		owners = append(owners, fqn+"-"+env)
	}
	for _, owner := range owners {
		for i := range labelled {
			e := &labelled[i]
			if e.Spec.Project == project && e.Spec.Service == owner {
				return e, nil
			}
		}
	}

	names := []string{env, fqn + "-" + env}
	if n, ok := strings.CutPrefix(env, "preview-pr-"); ok {
		names = append(names, fqn+"-pr-"+n)
	}
	for _, name := range names {
		e, gerr := c.GetKusoEnvironment(ctx, ns, name)
		if apierrors.IsNotFound(gerr) {
			continue
		}
		if gerr != nil {
			return nil, fmt.Errorf("get env %s: %w", name, gerr)
		}
		if e.Spec.Project == project && e.Spec.Service == fqn {
			return e, nil
		}
	}
	return nil, fmt.Errorf("%w: %q of %s", ErrEnvNotFound, env, fqn)
}

// ServiceEnvRef splits a resolved env into the (short service, env suffix)
// pair the name-composing APIs expect: "<project>-<short>-<suffix>" == e.Name.
// ok is false when the CR name doesn't follow that shape.
func ServiceEnvRef(project string, e *KusoEnvironment) (short, suffix string, ok bool) {
	suffix, ok = strings.CutPrefix(e.Name, e.Spec.Service+"-")
	if !ok || suffix == "" {
		return "", "", false
	}
	short, ok = strings.CutPrefix(e.Spec.Service, project+"-")
	if !ok || short == "" {
		return "", "", false
	}
	return short, suffix, true
}
