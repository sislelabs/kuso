package previewdb

import (
	"context"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/addons"
	"kuso/server/internal/kube"
	"kuso/server/internal/releaserun"
)

// selectMigratableEnvs picks the preview env CRs that should run a migration
// against a given clone after it is (re)seeded: those that mount the clone's
// conn secret, carry a release command (the migration), and already have an
// image to run it from. The preview env CR is the join table between a
// per-addon clone and the per-service release command + app image.
func selectMigratableEnvs(envs []kube.KusoEnvironment, cloneConn string) []kube.KusoEnvironment {
	var out []kube.KusoEnvironment
	for i := range envs {
		e := envs[i]
		if !envNeedsMigrate(&e, cloneConn) {
			continue
		}
		if e.Spec.Image == nil || e.Spec.Image.Tag == "" {
			continue
		}
		out = append(out, e)
	}
	return out
}

// envNeedsMigrate reports whether this env should run a migration against the
// clone once everything is ready: it mounts the clone's conn secret AND has a
// release hook. It does NOT require an image — the image is promoted by the
// build poller asynchronously and can land after the seed completes, so the
// migrate path waits for it rather than skipping. Used to decide whether to
// wait for an image at all (vs. an env that never migrates: no release, or a
// different clone).
func envNeedsMigrate(e *kube.KusoEnvironment, cloneConn string) bool {
	if e.Spec.Release == nil || len(e.Spec.Release.Command) == 0 {
		return false
	}
	return containsString(e.Spec.EnvFromSecrets, cloneConn)
}

func containsString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// buildMigrateJob renders the post-seed migration Job for one preview env
// against a freshly-(re)seeded clone. It runs the env's release command in the
// env's PR image, with the env's full envFrom mounted so DATABASE_URL resolves
// to the clone (the clone conn secret was swapped onto the env CR by the
// dispatcher).
//
// The Job name is keyed on the per-seed nonce (nowUnix), NOT on (env, image-
// tag): a close→reopen re-seeds the same image tag, and a tag-keyed name would
// fast-path to the stale prior run and skip re-migrating an already-wiped DB.
// A fresh nonce per seed guarantees the migration re-runs every time the DB is
// reset. One-shot (backoffLimit 0) — never retry a half-applied migration.
func buildMigrateJob(ns, project, cloneFQN string, env *kube.KusoEnvironment, ownerUID types.UID, nowUnix int64) *batchv1.Job {
	jobName := fmt.Sprintf("%s-migrate-%s-%d", cloneFQN, env.Name, nowUnix)
	if len(jobName) > 63 {
		jobName = jobName[:63]
	}

	backoff := int32(0)
	one := int32(1)
	ttl := int32(86400) // keep 24h for log access; resync nonces keep names unique

	var owners []metav1.OwnerReference
	if ownerUID != "" {
		blockFalse := false
		controllerFalse := false
		owners = append(owners, metav1.OwnerReference{
			APIVersion:         "application.kuso.sislelabs.com/v1alpha1",
			Kind:               "KusoAddon",
			Name:               cloneFQN,
			UID:                ownerUID,
			BlockOwnerDeletion: &blockFalse,
			Controller:         &controllerFalse,
		})
	}

	envFrom := make([]corev1.EnvFromSource, 0, len(env.Spec.EnvFromSecrets))
	for _, secret := range env.Spec.EnvFromSecrets {
		envFrom = append(envFrom, corev1.EnvFromSource{
			SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: secret},
				Optional:             ptrBool(true),
			},
		})
	}
	// Preserve valueFrom (addon-aliased secretKeyRefs) — see kube.CoreEnvVars.
	// Without this the post-seed migrate against the preview-DB clone reads an
	// empty DATABASE_URI and falls back to localhost.
	envVars := kube.CoreEnvVars(env.Spec.EnvVars)

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:            jobName,
			Namespace:       ns,
			OwnerReferences: owners,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by":   "kuso-server",
				"kuso.sislelabs.com/role":        "preview-migrate",
				"kuso.sislelabs.com/project":     project,
				"kuso.sislelabs.com/env":         env.Name,
				"kuso.sislelabs.com/clone-addon": addons.ShortName(project, cloneFQN),
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			Completions:             &one,
			Parallelism:             &one,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"kuso.sislelabs.com/role":                  "preview-migrate",
						"kuso.sislelabs.com/project":               project,
						"kuso.sislelabs.com/network-egress-public": "true",
					},
				},
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					AutomountServiceAccountToken: ptrBool(false),
					// TCP-wait on the clone DB before migrating — the clone
					// StatefulSet's ClusterIP transiently refuses connections
					// while it comes up / reconciles its Service. With
					// backoffLimit=0 a connection-refused would otherwise
					// permanently fail the migrate. Shared with the release Job.
					InitContainers: []corev1.Container{
						releaserun.WaitForAddonsInitContainer(envVars, envFrom),
					},
					Containers: []corev1.Container{
						{
							Name:    "migrate",
							Image:   fmt.Sprintf("%s:%s", env.Spec.Image.Repository, env.Spec.Image.Tag),
							Command: env.Spec.Release.Command,
							Env:     envVars,
							EnvFrom: envFrom,
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: ptrBool(false),
								Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("256Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("1Gi"),
								},
							},
						},
					},
					SecurityContext: &corev1.PodSecurityContext{
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
				},
			},
		},
	}
}

func ptrBool(b bool) *bool { return &b }

// tryAcquireSeed returns true if no seed+migrate is already in flight for this
// clone, marking it in-flight. ensurePreviewEnv calls EnsurePRAddons once per
// service, so without this guard every service that shares a DB addon would
// spawn its own seed+migrate goroutine for the SAME clone — producing
// redundant seed Jobs and migrate Jobs per reopen (observed: 3). The first
// caller wins; the rest skip. releaseSeed clears the flag when the
// seed+migrate finishes, so a later genuine resync can run again.
func (c *Cloner) tryAcquireSeed(cloneFQN string) bool {
	c.seedMu.Lock()
	defer c.seedMu.Unlock()
	if c.seedInFlight[cloneFQN] {
		return false
	}
	if c.seedInFlight == nil {
		c.seedInFlight = map[string]bool{}
	}
	c.seedInFlight[cloneFQN] = true
	return true
}

func (c *Cloner) releaseSeed(cloneFQN string) {
	c.seedMu.Lock()
	defer c.seedMu.Unlock()
	delete(c.seedInFlight, cloneFQN)
}

// migrateAfterSeed runs the release-hook migration against a freshly-(re)seeded
// clone, for every preview env in this PR that uses the clone. It is called
// from seedAsync AFTER the seed Job completes, so the migration always lands on
// top of the seeded schema (the seed's `pg_dump --clean` drops+recreates
// tables, so migrating before the seed would be wiped). This is the single
// owner of preview migrations — the build poller skips release Jobs for
// preview envs (see builds.Poller).
//
// Best-effort per env: a failed migration is logged (the preview still boots,
// just un-migrated, same contract as the seed). nonce is the seed's nowUnix so
// each (re)seed produces a distinct, non-deduped migrate Job.
func (c *Cloner) migrateAfterSeed(ctx context.Context, ns, project string, envScope string, cloneFQN string, nonce int64) {
	envs, err := c.Kube.ListKusoEnvironmentsByLabels(ctx, ns, map[string]string{
		kube.LabelEnv: envScope,
	})
	if err != nil {
		c.Logger.Warn("preview migrate: list envs", "clone", cloneFQN, "scope", envScope, "err", err)
		return
	}
	// Envs that SHOULD migrate against this clone (mount it + have a release
	// hook) — regardless of whether the image has landed yet. The build
	// poller promotes the image asynchronously and it can land AFTER the seed
	// completes, so we wait for it below rather than skipping the env.
	conn := addons.ConnSecretName(cloneFQN)
	var targets []kube.KusoEnvironment
	for i := range envs {
		if envNeedsMigrate(&envs[i], conn) {
			targets = append(targets, envs[i])
		}
	}
	if len(targets) == 0 {
		return // no service uses this clone with a release hook
	}

	var ownerUID types.UID
	if clone, err := c.Kube.GetKusoAddon(ctx, ns, cloneFQN); err == nil && clone != nil {
		ownerUID = clone.UID
	}

	for i := range targets {
		// Wait for the env's image to be promoted before migrating — the
		// build poller stamps spec.image asynchronously and it may not be
		// present yet when the seed finishes. Re-fetch the env CR until the
		// image lands (bounded); skip if it never does.
		env := c.waitForEnvImage(ctx, ns, targets[i].Name, 10*time.Minute)
		if env == nil {
			c.Logger.Warn("preview migrate: env image never promoted; skipping",
				"env", targets[i].Name, "clone", cloneFQN)
			continue
		}
		job := buildMigrateJob(ns, project, cloneFQN, env, ownerUID, nonce)
		if _, err := c.Kube.Clientset.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{}); err != nil {
			if apierrors.IsAlreadyExists(err) {
				// Same seed nonce already migrating this env — observe it.
			} else {
				c.Logger.Warn("preview migrate: create job", "env", env.Name, "clone", cloneFQN, "err", err)
				continue
			}
		}
		if err := c.waitForJobComplete(ctx, ns, job.Name, 10*time.Minute); err != nil {
			c.Logger.Warn("preview migrate failed", "env", env.Name, "clone", cloneFQN, "job", job.Name, "err", err)
			continue
		}
		c.Logger.Info("preview migrate applied", "env", env.Name, "clone", cloneFQN, "job", job.Name)
	}
}

// waitForEnvImage re-fetches the env CR until spec.image.tag is set (the build
// poller promoted it), or returns nil on timeout. Returns the env with the
// image so the caller can build the migrate Job against the PR image.
func (c *Cloner) waitForEnvImage(ctx context.Context, ns, envName string, timeout time.Duration) *kube.KusoEnvironment {
	deadline := time.Now().Add(timeout)
	for {
		env, err := c.Kube.GetKusoEnvironment(ctx, ns, envName)
		if err == nil && env != nil && env.Spec.Image != nil && env.Spec.Image.Tag != "" {
			return env
		}
		if time.Now().After(deadline) {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(3 * time.Second):
		}
	}
}

// waitForJobComplete polls until the Job reaches JobComplete, or returns an
// error if it fails or the timeout elapses.
func (c *Cloner) waitForJobComplete(ctx context.Context, ns, jobName string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		job, err := c.Kube.Clientset.BatchV1().Jobs(ns).Get(ctx, jobName, metav1.GetOptions{})
		if err == nil {
			for _, cond := range job.Status.Conditions {
				if cond.Type == batchv1.JobComplete && cond.Status == corev1.ConditionTrue {
					return nil
				}
				if cond.Type == batchv1.JobFailed && cond.Status == corev1.ConditionTrue {
					return fmt.Errorf("migrate job failed: %s", cond.Message)
				}
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("migrate job %s did not complete within %s", jobName, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}
