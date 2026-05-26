// Package releaserun runs the optional pre-deploy release Job that
// gates image promotion on the build poller. Heroku-style release
// phase: ./bin/migrate runs as a Job against the NEW build's image
// before the env's deployment ever sees the new tag. On Job failure
// the image tag is NOT patched; existing pods keep running the
// previous image.
//
// Why a separate package: the build poller is already 2k lines and
// the Job-template + poll + log-tail concerns are independent enough
// to live on their own. The poller calls Run() from its promote
// path and treats the result as a binary go/no-go.
package releaserun

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// Outcome captures the terminal state of a release Job.
type Outcome string

const (
	// OutcomeSucceeded — Job completed with exit code 0. Build poller
	// patches the image tag onto the env.
	OutcomeSucceeded Outcome = "succeeded"
	// OutcomeFailed — Job completed with non-zero exit. Build poller
	// marks the build release-failed and skips the image patch.
	OutcomeFailed Outcome = "failed"
	// OutcomeTimedOut — Job exceeded its activeDeadlineSeconds. Kube
	// auto-terminated it; we treat this the same as Failed for the
	// promote gate but the notify event distinguishes it.
	OutcomeTimedOut Outcome = "timed-out"
)

// Result is what Run() returns. JobName lets callers fetch logs after
// the fact via Logs().
type Result struct {
	Outcome  Outcome
	JobName  string
	ExitCode int32
	Message  string
}

// Runner runs release Jobs. Created once per server boot; reused
// across calls. The Kube client + project namespace resolver are
// shared with the rest of the server.
type Runner struct {
	Kube *kube.Client
}

// New constructs a Runner. Kube is required.
func New(k *kube.Client) *Runner {
	return &Runner{Kube: k}
}

// Run executes the release hook for one env + image. It's synchronous
// — the caller polls inside this call. Idempotency: Job name is
// derived from env + image tag, so two callers (or a retry) for the
// same (env, image) reuse the same Job and observe the same outcome.
//
// Preconditions:
//   - env.Spec.Release.Command must be non-empty (caller's gate).
//   - image.Tag must be non-empty (the build poller fills this in).
//
// On any return Result.JobName is set so the caller can deep-link.
func (r *Runner) Run(ctx context.Context, ns string, env *kube.KusoEnvironment, image *kube.KusoImage) (Result, error) {
	if env == nil || env.Spec.Release == nil || len(env.Spec.Release.Command) == 0 {
		return Result{}, fmt.Errorf("releaserun: env has no release.command")
	}
	if image == nil || image.Tag == "" {
		return Result{}, fmt.Errorf("releaserun: image tag required")
	}
	jobName := JobName(env.Name, image.Tag)
	timeout := env.Spec.Release.TimeoutSeconds
	if timeout <= 0 {
		timeout = 900
	}

	// Fast-path: a prior run for the same (env, tag) already
	// succeeded. Re-deploys of the same image should not re-run
	// migrations. If the Job exists in any phase, observe it
	// rather than create a new one.
	existing, gerr := r.Kube.Clientset.BatchV1().Jobs(ns).Get(ctx, jobName, metav1.GetOptions{})
	if gerr == nil {
		return r.poll(ctx, ns, jobName, time.Duration(timeout)*time.Second)
	}
	if !apierrors.IsNotFound(gerr) {
		return Result{JobName: jobName}, fmt.Errorf("get release job: %w", gerr)
	}

	job := r.buildJob(env, image, jobName, int32(timeout))
	if _, err := r.Kube.Clientset.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Race: another caller created it between our GET and
			// CREATE. Fall through to polling the one that won.
			_ = existing
		} else {
			return Result{JobName: jobName}, fmt.Errorf("create release job: %w", err)
		}
	}

	return r.poll(ctx, ns, jobName, time.Duration(timeout)*time.Second)
}

// JobName derives the per-(env, tag) Job name. Exported so the build
// poller can reference it in CR annotations + the web layer can deep-
// link to logs without re-deriving.
func JobName(envName, imageTag string) string {
	short := imageTag
	if len(short) > 12 {
		short = short[:12]
	}
	// Strip anything that isn't kube-name-safe — image tags can carry
	// colons/dots from the registry path; strip to be safe.
	short = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		return '-'
	}, strings.ToLower(short))
	name := fmt.Sprintf("%s-release-%s", envName, short)
	if len(name) > 63 {
		name = name[:63]
	}
	return strings.TrimRight(name, "-")
}

// buildJob renders the kube Job spec. The pod inherits the env's
// resolved envVars + envFromSecrets so the release command sees the
// same DATABASE_URL etc. that the running pods do.
func (r *Runner) buildJob(env *kube.KusoEnvironment, image *kube.KusoImage, name string, timeoutSec int32) *batchv1.Job {
	envVars := make([]corev1.EnvVar, 0, len(env.Spec.EnvVars))
	for _, e := range env.Spec.EnvVars {
		// Closed schema rejects valueFrom smuggling at the apiserver
		// already; we trust what landed on the env CR.
		ev := corev1.EnvVar{Name: e.Name, Value: e.Value}
		envVars = append(envVars, ev)
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

	backoff := int32(0) // One shot — never retry a failed migration.
	parallelism := int32(1)
	completions := int32(1)

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: env.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":          "release-job",
				"app.kubernetes.io/managed-by":    "kuso-server",
				"kuso.sislelabs.com/project":      env.Spec.Project,
				"kuso.sislelabs.com/service":      env.Spec.Service,
				"kuso.sislelabs.com/env":          env.Name,
				"kuso.sislelabs.com/release-tag":  image.Tag,
				"kuso.sislelabs.com/role":         "release",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "application.kuso.sislelabs.com/v1alpha1",
					Kind:               "KusoEnvironment",
					Name:               env.Name,
					UID:                env.UID,
					BlockOwnerDeletion: ptrBool(false),
					Controller:         ptrBool(false),
				},
			},
		},
		Spec: batchv1.JobSpec{
			ActiveDeadlineSeconds: ptrInt64(int64(timeoutSec)),
			BackoffLimit:          &backoff,
			Parallelism:           &parallelism,
			Completions:           &completions,
			TTLSecondsAfterFinished: ptrInt32(86400), // Keep Job + pod 24h for log access.
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"kuso.sislelabs.com/role":    "release",
						"kuso.sislelabs.com/project": env.Spec.Project,
						"kuso.sislelabs.com/service": env.Spec.Service,
						// release Jobs are short-lived; grant the same
						// public-egress so curl-based migrations (rare
						// but real) can reach package CDNs etc.
						"kuso.sislelabs.com/network-egress-public": "true",
					},
				},
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					AutomountServiceAccountToken: ptrBool(false),
					Containers: []corev1.Container{
						{
							Name:    "release",
							Image:   fmt.Sprintf("%s:%s", image.Repository, image.Tag),
							Command: env.Spec.Release.Command,
							Env:     envVars,
							EnvFrom: envFrom,
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: ptrBool(false),
								Capabilities: &corev1.Capabilities{
									Drop: []corev1.Capability{"ALL"},
								},
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

// poll waits for the Job to finish or timeout. Returns the terminal
// outcome. Polls every 2s — release Jobs are typically 10-60s, this
// keeps the apiserver load tiny while latency stays sub-3s.
func (r *Runner) poll(ctx context.Context, ns, jobName string, timeout time.Duration) (Result, error) {
	// Belt-and-braces upper bound. The Job's own activeDeadlineSeconds
	// is the primary timeout; this protects against a watch hang.
	overall := timeout + 30*time.Second
	deadline := time.Now().Add(overall)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		job, err := r.Kube.Clientset.BatchV1().Jobs(ns).Get(ctx, jobName, metav1.GetOptions{})
		if err != nil {
			return Result{JobName: jobName}, fmt.Errorf("get release job: %w", err)
		}
		for _, c := range job.Status.Conditions {
			switch c.Type {
			case batchv1.JobComplete:
				if c.Status == corev1.ConditionTrue {
					return Result{
						Outcome: OutcomeSucceeded,
						JobName: jobName,
					}, nil
				}
			case batchv1.JobFailed:
				if c.Status == corev1.ConditionTrue {
					if strings.EqualFold(c.Reason, "DeadlineExceeded") {
						return Result{
							Outcome: OutcomeTimedOut,
							JobName: jobName,
							Message: c.Message,
						}, nil
					}
					return Result{
						Outcome: OutcomeFailed,
						JobName: jobName,
						Message: c.Message,
					}, nil
				}
			}
		}
		if time.Now().After(deadline) {
			return Result{
				Outcome: OutcomeTimedOut,
				JobName: jobName,
				Message: "release-runner: poll deadline exceeded",
			}, nil
		}
		select {
		case <-ctx.Done():
			return Result{JobName: jobName}, ctx.Err()
		case <-ticker.C:
		}
	}
}

// Logs returns a stream of the most recent release Job pod's logs for
// (env, imageTag). Caller closes the stream. Returns ErrNoJob if the
// Job hasn't been created (or has been GC'd).
func (r *Runner) Logs(ctx context.Context, ns, envName, imageTag string) (string, error) {
	jobName := JobName(envName, imageTag)
	pods, err := r.Kube.Clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("job-name=%s", jobName),
	})
	if err != nil {
		return "", fmt.Errorf("list release pods: %w", err)
	}
	if len(pods.Items) == 0 {
		return "", ErrNoJob
	}
	// Pick the newest pod (Jobs with backoffLimit=0 should only ever
	// have one, but be defensive).
	pick := pods.Items[0]
	for _, p := range pods.Items[1:] {
		if p.CreationTimestamp.After(pick.CreationTimestamp.Time) {
			pick = p
		}
	}
	req := r.Kube.Clientset.CoreV1().Pods(ns).GetLogs(pick.Name, &corev1.PodLogOptions{})
	stream, err := req.Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("stream release logs: %w", err)
	}
	defer stream.Close()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, rerr := stream.Read(buf)
		if n > 0 {
			b.Write(buf[:n])
		}
		if rerr != nil {
			break
		}
	}
	return b.String(), nil
}

// ErrNoJob is returned by Logs when the release Job hasn't been
// created (or has been TTL-garbage-collected).
var ErrNoJob = errors.New("releaserun: no job for env+tag")

func ptrBool(b bool) *bool       { return &b }
func ptrInt32(i int32) *int32    { return &i }
func ptrInt64(i int64) *int64    { return &i }
