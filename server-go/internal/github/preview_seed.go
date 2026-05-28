// Preview-env seed Job orchestration (v0.17.0 Phase 2).
//
// When a service declares spec.previews.seed = "<cmd>" kuso schedules
// a one-shot kube Job after the preview env's addons reach Ready.
// The Job runs in a clone of the build image so it has access to
// the same node_modules / vendored deps / package scripts the runtime
// pod uses. Env vars are the merged baseEnv + previewEnvVars set we
// computed during ensurePreviewEnv, so the seed script sees exactly
// the same DATABASE_URL / REDIS_URL the preview pod will.
//
// Status flow:
//   pending  → Job spec submitted, no pod yet
//   running  → seed pod active
//   succeeded → Job.status.succeeded == 1
//   failed    → Job.status.failed >= backoffLimit (3) OR timeout
//
// Status is persisted as an annotation on the env CR so the reviewer
// page can read it without keeping a polling channel open.

package github

import (
	"context"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/kube"
)

const kubeMergePatch = types.MergePatchType

func envGVR() schema.GroupVersionResource { return kube.GVREnvironments }
func boolPtr(b bool) *bool                { return &b }

// BaseCtxOrTODO returns a context the goroutine can survive on after
// the request context cancels. Currently context.TODO; can be swapped
// to a longer-lived dispatcher context if we add one.
func (d *Dispatcher) BaseCtxOrTODO() context.Context { return context.TODO() }

const (
	// AnnPreviewSeedPhase carries the seed Job's status so the
	// reviewer page can render "seeding…" / "ready" / "seed failed"
	// without scraping the kube Job state directly. Set by the
	// goroutine that polls the Job after submission.
	AnnPreviewSeedPhase = "kuso.sislelabs.com/preview-seed-phase"
	AnnPreviewSeedAt    = "kuso.sislelabs.com/preview-seed-at"
	AnnPreviewSeedError = "kuso.sislelabs.com/preview-seed-error"
)

// runPreviewSeedJob schedules the user-defined seed command as a
// one-shot kube Job. Safe to call multiple times — the Job name
// includes a timestamp so re-runs (PR resync) produce a fresh Job
// without colliding. Status is stamped on the env CR's annotations
// via a polling goroutine.
//
// Returns immediately after the Job spec is submitted; the actual
// seed runs asynchronously. Errors here mean we couldn't even submit
// the Job (typically: kube apiserver unreachable, namespace gone).
func (d *Dispatcher) runPreviewSeedJob(ctx context.Context, project, envCRName, image, seedCmd string, envFromSecrets []string, envVars []envVar) error {
	if seedCmd == "" {
		return nil
	}
	ns := d.nsFor(ctx, project)
	jobName := fmt.Sprintf("%s-seed-%d", envCRName, time.Now().Unix())

	// Convert our typed envVars list into the kube shape. Same shape
	// the runtime pod uses, just without the kube.KusoEnvVar typed
	// wrapper that the chart needs.
	podEnv := make([]corev1.EnvVar, 0, len(envVars))
	for _, ev := range envVars {
		podEnv = append(podEnv, ev.toCore())
	}

	envFrom := make([]corev1.EnvFromSource, 0, len(envFromSecrets))
	for _, name := range envFromSecrets {
		envFrom = append(envFrom, corev1.EnvFromSource{
			SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: name},
				Optional:             boolPtr(true),
			},
		})
	}

	backoff := int32(3)
	// 10-minute deadline. Past that the operator's app probably has a
	// stuck seed (migration deadlock, queryland infinite loop). Better
	// to fail loudly than to silently chew compute.
	deadline := int64(10 * 60)
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: jobName,
			Labels: map[string]string{
				"kuso.sislelabs.com/project":         project,
				"kuso.sislelabs.com/preview-seed":    envCRName,
				"app.kubernetes.io/managed-by":       "kuso-preview-seed",
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:          &backoff,
			ActiveDeadlineSeconds: &deadline,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"kuso.sislelabs.com/preview-seed": envCRName,
					},
				},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyOnFailure,
					Containers: []corev1.Container{{
						Name:    "seed",
						Image:   image,
						Command: []string{"sh", "-c"},
						Args:    []string{seedCmd},
						Env:     podEnv,
						EnvFrom: envFrom,
					}},
				},
			},
		},
	}
	if _, err := d.Kube.Clientset.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create seed job: %w", err)
		}
	}
	d.markEnvSeed(ctx, ns, envCRName, "pending", "")

	// Poll for completion in the background. Bounded by the Job's
	// activeDeadlineSeconds (10min) + a small buffer.
	go d.watchSeedJob(d.BaseCtxOrTODO(), ns, envCRName, jobName)
	return nil
}

// envVar is the dispatcher's local typed env-var shape so we don't
// have to import the kube package's KusoEnvVar (which carries a
// map[string]any ValueFrom and isn't directly convertible to
// corev1.EnvVar). One-way conversion only — the dispatcher writes
// envs to the env CR via the kube types, AND submits them to a
// kube Job via corev1 here.
type envVar struct {
	name      string
	value     string
	secretRef *envVarSecretRef
}

type envVarSecretRef struct {
	secretName string
	key        string
}

func (e envVar) toCore() corev1.EnvVar {
	if e.secretRef != nil {
		return corev1.EnvVar{
			Name: e.name,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: e.secretRef.secretName},
					Key:                  e.secretRef.key,
					Optional:             boolPtr(true),
				},
			},
		}
	}
	return corev1.EnvVar{Name: e.name, Value: e.value}
}

// markEnvSeed stamps the seed phase on the env CR via a merge-patch.
// Annotation-based so multiple seed runs (on resync) overwrite cleanly
// without spec churn.
func (d *Dispatcher) markEnvSeed(ctx context.Context, ns, envCRName, phase, errMsg string) {
	patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:%q,%q:%q,%q:%q}}}`,
		AnnPreviewSeedPhase, phase,
		AnnPreviewSeedAt, time.Now().UTC().Format(time.RFC3339),
		AnnPreviewSeedError, errMsg,
	)
	_, err := d.Kube.Dynamic.Resource(envGVR()).Namespace(ns).
		Patch(ctx, envCRName, kubeMergePatch, []byte(patch), metav1.PatchOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		d.Logger.Warn("preview seed: mark phase", "env", envCRName, "phase", phase, "err", err)
	}
}

// watchSeedJob polls the Job until success/fail, then updates the
// env CR's seed annotations + cleans up the Job. Bounded by the
// Job's activeDeadlineSeconds.
func (d *Dispatcher) watchSeedJob(ctx context.Context, ns, envCRName, jobName string) {
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	deadline := time.Now().Add(15 * time.Minute)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if time.Now().After(deadline) {
			d.markEnvSeed(ctx, ns, envCRName, "failed", "watch timeout")
			return
		}
		job, err := d.Kube.Clientset.BatchV1().Jobs(ns).Get(ctx, jobName, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return
			}
			continue
		}
		if job.Status.Active > 0 && d.envSeedPhase(ctx, ns, envCRName) != "running" {
			d.markEnvSeed(ctx, ns, envCRName, "running", "")
		}
		if job.Status.Succeeded > 0 {
			d.markEnvSeed(ctx, ns, envCRName, "succeeded", "")
			return
		}
		if job.Status.Failed >= 3 {
			msg := "seed job failed after 3 attempts"
			for _, c := range job.Status.Conditions {
				if c.Type == batchv1.JobFailed && c.Message != "" {
					msg = c.Message
					break
				}
			}
			d.markEnvSeed(ctx, ns, envCRName, "failed", msg)
			return
		}
	}
}

// envSeedPhase reads the current seed phase annotation. Best-effort;
// "" on any error.
func (d *Dispatcher) envSeedPhase(ctx context.Context, ns, envCRName string) string {
	env, err := d.Kube.Dynamic.Resource(envGVR()).Namespace(ns).Get(ctx, envCRName, metav1.GetOptions{})
	if err != nil || env == nil {
		return ""
	}
	anns := env.GetAnnotations()
	if anns == nil {
		return ""
	}
	return anns[AnnPreviewSeedPhase]
}
