package builds

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/kube"
)

// AnnRestartedAt is the pod-template annotation a restart bumps. Same
// key the kuso-server / bot self-restarts use (kubectl rollout restart
// semantics under our own prefix).
const AnnRestartedAt = "kuso.sislelabs.com/restartedAt"

// Restart rolls one env's pods on the image they already run — no
// build, no promotion. The env chart renders a Deployment named after
// the env CR; stamping its pod template makes kube roll it with the
// chart's zero-downtime strategy. helm-operator's reconcile leaves the
// extra annotation alone because the chart never renders that key.
func (s *Service) Restart(ctx context.Context, project, service, env string) (time.Time, *kube.KusoEnvironment, error) {
	ns := s.nsFor(ctx, project)
	e, err := s.resolveEnv(ctx, ns, project, service, env)
	if err != nil {
		return time.Time{}, nil, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	patch := fmt.Appendf(nil, `{"spec":{"template":{"metadata":{"annotations":{%q:%q}}}}}`,
		AnnRestartedAt, now.Format(time.RFC3339))
	if _, err := s.Kube.Clientset.AppsV1().Deployments(ns).
		Patch(ctx, e.Name, types.StrategicMergePatchType, patch, metav1.PatchOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return time.Time{}, nil, fmt.Errorf("%w: environment %s has no deployment yet (awaiting its first build)", ErrInvalid, envGroupName(e, project+"-"+service))
		}
		return time.Time{}, nil, fmt.Errorf("restart deployment %s: %w", e.Name, err)
	}
	return now, e, nil
}
