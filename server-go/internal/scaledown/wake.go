package scaledown

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/kube"
)

// Wake restores a slept env to its pre-sleep replica count. Shared by the
// activator (wake on request) and the watcher (wake an env that is asleep
// but no longer allowed to sleep, e.g. the service just opted out).
//
// Env CR first, Deployment second: the last-activity stamp must be visible
// before replicas go up, or a scaledown tick in the cold-start window sees
// a running "idle" env and re-sleeps it mid-hold. Persisting replicaCount
// stops the helm-operator reverting a static-replica env to 0; for an HPA
// env the chart omits spec.replicas, so raising the Deployment above 0 is
// what reactivates the (implicitly paused) HPA.
func Wake(ctx context.Context, kc *kube.Client, logger *slog.Logger, ns, name string, now time.Time) error {
	if kc == nil || kc.Clientset == nil {
		return fmt.Errorf("scaledown: no kube client")
	}
	if logger == nil {
		logger = slog.Default()
	}
	want := preSleepReplicas(ctx, kc, ns, name)

	if _, uerr := kc.UpdateKusoEnvironmentWithRetry(ctx, ns, name, func(e *kube.KusoEnvironment) error {
		if e.Spec.ReplicaCountValue() < want {
			e.Spec.SetReplicaCount(want)
		}
		if e.Annotations == nil {
			e.Annotations = map[string]string{}
		}
		e.Annotations[LastActivityAnnotation] = now.UTC().Format(time.RFC3339)
		delete(e.Annotations, PreSleepReplicasAnnotation)
		return nil
	}); uerr != nil {
		logger.Warn("scaledown: persist wake on env", "ns", ns, "env", name, "err", uerr)
	}

	patch := []byte(fmt.Sprintf(`{"spec":{"replicas":%d}}`, want))
	_, err := kc.Clientset.AppsV1().Deployments(ns).Patch(
		ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("scale deployment: %w", err)
	}
	return nil
}

// preSleepReplicas reads the count scaledown stashed before sleeping the
// env. 1 when missing or unparseable — the floor that always serves.
func preSleepReplicas(ctx context.Context, kc *kube.Client, ns, name string) int {
	env, err := kc.GetKusoEnvironment(ctx, ns, name)
	if err != nil || env == nil {
		return 1
	}
	if v := env.Annotations[PreSleepReplicasAnnotation]; v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			return n
		}
	}
	return 1
}
