package handlers

import (
	"context"
	"strings"

	"kuso/server/internal/kube"
)

// envGroupTarget redirects (service, env) to the env-group clone that
// actually carries env: `web --env staging` is served by KusoService
// web-staging, whose env CR is web-staging-production. It returns the
// inputs unchanged unless the resolver lands on a different service, so
// every non-group request keeps its existing lookup and error shape.
func envGroupTarget(ctx context.Context, k *kube.Client, homeNS, project, service, env string) (string, string) {
	if k == nil || env == "" || strings.HasPrefix(env, "build:") || strings.HasPrefix(env, "run:") {
		return service, env
	}
	ns := homeNS
	proj, err := k.GetKusoProject(ctx, homeNS, project)
	if err != nil {
		return service, env
	}
	if proj.Spec.Namespace != "" {
		ns = proj.Spec.Namespace
	}
	e, err := k.ResolveServiceEnv(ctx, ns, project, service, env)
	if err != nil {
		return service, env
	}
	fqn := service
	if !strings.HasPrefix(service, project+"-") {
		fqn = project + "-" + service
	}
	if e.Spec.Service == fqn {
		return service, env
	}
	short, suffix, ok := kube.ServiceEnvRef(project, e)
	if !ok {
		return service, env
	}
	return short, suffix
}
