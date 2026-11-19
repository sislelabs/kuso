package projects

import (
	"context"

	"kuso/server/internal/kube"
)

// putEnvFixture overwrites a seeded env CR's labels, annotations and spec.
func putEnvFixture(ctx context.Context, k *kube.Client, ns string, e *kube.KusoEnvironment) (*kube.KusoEnvironment, error) {
	return k.UpdateKusoEnvironmentWithRetry(ctx, ns, e.Name, func(live *kube.KusoEnvironment) error {
		live.Labels, live.Annotations, live.Spec = e.Labels, e.Annotations, e.Spec
		return nil
	})
}

// putServiceFixture overwrites a seeded service CR's labels, annotations and spec.
func putServiceFixture(ctx context.Context, k *kube.Client, ns string, svc *kube.KusoService) (*kube.KusoService, error) {
	return k.UpdateKusoServiceWithRetry(ctx, ns, svc.Name, func(live *kube.KusoService) error {
		live.Labels, live.Annotations, live.Spec = svc.Labels, svc.Annotations, svc.Spec
		return nil
	})
}
