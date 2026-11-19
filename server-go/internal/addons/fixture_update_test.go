package addons

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
