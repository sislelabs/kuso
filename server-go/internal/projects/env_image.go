package projects

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"kuso/server/internal/imagerelease"
	"kuso/server/internal/kube"
	"kuso/server/internal/registryauth"
)

const revOpEnvImage = "environment.image"

// resolveServiceEnv finds project/service's env CR for env, which is the
// env-group label the UI sends ("production", "staging", "preview-pr-7")
// or the CR suffix. Empty means production.
func (s *Service) resolveServiceEnv(ctx context.Context, ns, project, service, env string) (*kube.KusoEnvironment, error) {
	if env == "" {
		env = "production"
	}
	short := strings.TrimPrefix(service, project+"-")
	fqn := project + "-" + short
	envs, err := s.Kube.ListKusoEnvironmentsByLabels(ctx, ns, map[string]string{labelProject: project, labelService: short})
	if err != nil {
		return nil, fmt.Errorf("list envs for %s/%s: %w", project, short, err)
	}
	for i := range envs {
		if envs[i].Spec.Service != fqn {
			continue
		}
		if envs[i].Labels[labelEnv] == env || envs[i].Name == envCRNameFor(project, short, env) {
			return &envs[i], nil
		}
	}
	return nil, fmt.Errorf("%w: environment %q of %s/%s", ErrNotFound, env, project, short)
}

// SetEnvironmentImage points ONE environment of a runtime=image service at
// repository:tag, leaving the service spec (and every other env) alone.
// It is the env-scoped counterpart of saving the image tag: with a release
// hook the image waits in pendingImage for the hook, like propagation does.
func (s *Service) SetEnvironmentImage(ctx context.Context, project, service, env, repository, tag string) error {
	repository = strings.TrimSpace(repository)
	tag = strings.TrimSpace(tag)
	if repository == "" {
		return fmt.Errorf("%w: image repository is required", ErrInvalid)
	}
	if tag == "" {
		tag = "latest"
	}
	svc, err := s.GetService(ctx, project, service)
	if err != nil {
		return err
	}
	if svc.Spec.Runtime != "image" {
		return fmt.Errorf("%w: %s builds from source; roll back to a build instead", ErrInvalid, service)
	}
	if registryauth.ForeignRepo(project, repository) && (svc.Spec.Image == nil || svc.Spec.Image.Repository != repository) {
		return fmt.Errorf("%w: %s", ErrInvalid, registryauth.ForeignRepoReason("image repository", project))
	}
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return err
	}
	cur, err := s.resolveServiceEnv(ctx, ns, project, service, env)
	if err != nil {
		return err
	}
	ctx, scope := s.beginRevision(ctx)
	hook := svc.Spec.Release != nil && len(svc.Spec.Release.Command) > 0
	var before string
	_, err = s.Kube.UpdateKusoEnvironmentWithRetry(ctx, ns, cur.Name, func(e *kube.KusoEnvironment) error {
		img := kube.KusoImage{Repository: repository, Tag: tag}
		// Keep the pull policy and registry credential the env runs with.
		if base := e.Spec.Image; base != nil {
			img.PullPolicy, img.PullSecret = base.PullPolicy, base.PullSecret
		} else if svc.Spec.Image != nil {
			img.PullPolicy, img.PullSecret = svc.Spec.Image.PullPolicy, svc.Spec.Image.PullSecret
		}
		if e.Spec.Image != nil {
			before = e.Spec.Image.Repository + ":" + e.Spec.Image.Tag
		}
		if hook {
			e.Spec.PendingImage = &img
			delete(e.Annotations, imagerelease.AnnFailedImage)
			delete(e.Annotations, imagerelease.AnnFailedAttempts)
			delete(e.Annotations, imagerelease.AnnFailedAt)
			return nil
		}
		if !reflect.DeepEqual(e.Spec.Image, &img) {
			kube.StampPromotedAt(e, time.Now())
		}
		e.Spec.Image = &img
		e.Spec.PendingImage = nil
		return nil
	})
	if err != nil {
		return fmt.Errorf("set env image: %w", err)
	}
	group := cur.Labels[labelEnv]
	if group == "" {
		group = strings.TrimPrefix(cur.Name, cur.Spec.Service+"-")
	}
	snap := revisionSnapshot{Op: revOpEnvImage, Image: &kube.KusoImage{Repository: repository, Tag: tag}}
	if svcPrefix := cur.Spec.Service + "-"; strings.HasPrefix(cur.Name, svcPrefix) {
		snap.Service = strings.TrimPrefix(cur.Spec.Service, project+"-")
		snap.Env = strings.TrimPrefix(cur.Name, svcPrefix)
	} else {
		snap.Informational = true
	}
	if before == "" {
		before = "none"
	}
	scope.record(ctx, project, "environment", strings.TrimPrefix(cur.Name, project+"-"),
		fmt.Sprintf("env %s: image %s → %s:%s", group, before, repository, tag), snap)
	return nil
}
