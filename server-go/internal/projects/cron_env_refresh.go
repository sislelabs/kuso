package projects

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"kuso/server/internal/crons"
	"kuso/server/internal/kube"
)

// refreshServiceCrons re-stamps env + envFromSecrets on every service cron
// from the service's production env. Crons snapshot both at create and were
// otherwise only refreshed when a build promoted a NEW image tag, so an env
// var, secret mount or addon change never reached the nightly job. The image
// is left alone (build promotion / `kuso cron sync` own it, and pinImage
// pins it), so pinned crons get the env refresh too.
func (s *Service) refreshServiceCrons(ctx context.Context, ns, project, service string) error {
	short := strings.TrimPrefix(service, project+"-")
	fqn := serviceCRName(project, short)
	prod, err := s.Kube.GetKusoEnvironment(ctx, ns, productionEnvName(project, short))
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get production env: %w", err)
	}
	if prod.Spec.Project != "" && prod.Spec.Project != project {
		return nil
	}
	list, err := s.Kube.ListKusoCrons(ctx, ns)
	if err != nil {
		return fmt.Errorf("list crons: %w", err)
	}
	envFrom := slices.Clone(prod.Spec.EnvFromSecrets)
	env := crons.EnvForCron(prod.Spec.EnvVars)
	var errs []error
	for i := range list {
		c := &list[i]
		if c.Spec.Project != project || (c.Spec.Kind != "" && c.Spec.Kind != "service") {
			continue
		}
		if c.Spec.Service != fqn && c.Spec.Service != short {
			continue
		}
		if slices.Equal(c.Spec.EnvFromSecrets, envFrom) && reflect.DeepEqual(c.Spec.Env, env) {
			continue
		}
		if _, uerr := s.Kube.UpdateKusoCronWithRetry(ctx, ns, c.Name, func(live *kube.KusoCron) error {
			live.Spec.EnvFromSecrets = slices.Clone(envFrom)
			live.Spec.Env = env
			return nil
		}); uerr != nil {
			errs = append(errs, fmt.Errorf("update cron %s: %w", c.Name, uerr))
		}
	}
	return errors.Join(errs...)
}
