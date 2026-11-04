package scaledown

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
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

	// Only raise a Deployment that is actually at 0. The "test" op makes
	// the scale-up conditional server-side: an awake env (a request that
	// arrived while endpoints were briefly unready) must never be patched
	// down to the pre-sleep count, which is 1 when the annotation is
	// absent. A JSON patch rather than GET+patch because the activator's
	// RBAC only grants deployments patch.
	patch := []byte(fmt.Sprintf(
		`[{"op":"test","path":"/spec/replicas","value":0},{"op":"replace","path":"/spec/replicas","value":%d}]`, want))
	_, err := kc.Clientset.AppsV1().Deployments(ns).Patch(
		ctx, name, types.JSONPatchType, patch, metav1.PatchOptions{})
	if err != nil && !apierrors.IsNotFound(err) && !replicasTestFailed(err) {
		return fmt.Errorf("scale deployment: %w", err)
	}
	return nil
}

// replicasTestFailed reports whether a Wake patch was refused because the
// Deployment is not at 0 replicas (already awake). The apiserver maps a
// failed JSON-patch test to 422; the fake client passes the jsonpatch error
// through verbatim.
func replicasTestFailed(err error) bool {
	return apierrors.IsInvalid(err) || strings.Contains(err.Error(), "testing value")
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
