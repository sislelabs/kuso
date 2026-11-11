package updater

import (
	"context"
	"os"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// updateJobDeadline bounds an updater Job. Without it a pod stuck in
// ImagePullBackOff (or a hung download) never counts as failed and
// blocks every later StartUpdate. Sized above the entrypoint's worst
// case: bounded downloads plus server/activator/operator rolls and
// their reverts.
const updateJobDeadline = 45 * time.Minute

// batchv1Job is the small bag of fields applyJob needs. Avoids
// passing the full corev1/batchv1 types into updater.go and keeps
// that file focused on policy.
type batchv1Job struct {
	Name      string
	Namespace string
	Image     string
	Env       map[string]string
}

// applyJob creates the kube Job that drives the upgrade. The Job
// runs as the kuso-server ServiceAccount, which can apply CRDs, roll
// Deployments/Jobs and write RBAC objects, but Kubernetes' escalation
// prevention refuses any role granting a permission kuso-server doesn't
// itself hold cluster-wide (see ESCALATION_EXCLUDED in hack/release.sh).
// The entrypoint's upgrade-manifests apply is therefore best-effort: a
// forbidden apply is reported in the final "done" status message and
// the image roll proceeds, so a release that touches RBAC can't brick
// self-update.
func (s *Service) applyJob(ctx context.Context, j *batchv1Job) error {
	bf := int32(0)
	one := int32(1)
	ttl := int32(86400)
	deadline := int64(updateJobDeadline.Seconds())
	noEscalation := false
	env := make([]corev1.EnvVar, 0, len(j.Env))
	for k, v := range j.Env {
		env = append(env, corev1.EnvVar{Name: k, Value: v})
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      j.Name,
			Namespace: j.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "kuso-server",
				"kuso.sislelabs.com/role":      "updater",
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &bf,
			ActiveDeadlineSeconds:   &deadline,
			Completions:             &one,
			Parallelism:             &one,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"kuso.sislelabs.com/role": "updater",
					},
				},
				Spec: corev1.PodSpec{
					RestartPolicy:      corev1.RestartPolicyNever,
					ServiceAccountName: "kuso-server",
					PriorityClassName:  s.platformPriorityClass(ctx),
					Containers: []corev1.Container{{
						Name:            "updater",
						Image:           j.Image,
						ImagePullPolicy: updaterPullPolicy(j.Image),
						Env:             env,
						// Stays root: the image has no non-root user, and a
						// spec the image can't run under would brick
						// self-update with no in-band recovery.
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: &noEscalation,
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
							SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
						},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("50m"),
								corev1.ResourceMemory: resource.MustParse("64Mi"),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceMemory: resource.MustParse("512Mi"),
							},
						},
					}},
				},
			},
		},
	}
	_, err := s.Kube.Clientset.BatchV1().Jobs(j.Namespace).Create(ctx, job, metav1.CreateOptions{})
	return err
}

// platformPriorityClass is the PriorityClass for the updater pod: with
// BackoffLimit 0, a kubelet eviction under node pressure mid-roll fails
// the Job with no retry and no revert. Empty when the class is missing
// or unreadable: the admission plugin rejects a pod naming an unknown
// class, which would brick self-update on installs that predate it.
func (s *Service) platformPriorityClass(ctx context.Context) string {
	const name = "kuso-platform"
	if _, err := s.Kube.Clientset.SchedulingV1().PriorityClasses().Get(ctx, name, metav1.GetOptions{}); err != nil {
		return ""
	}
	return name
}

// getenv is the package-local fallback for envOrDefault. We don't
// import os in updater.go because the file has enough imports
// already; this lets it stay readable.
func getenv(key string) string {
	return os.Getenv(key)
}

// updaterPullPolicy: a digest ref is immutable, so a cached copy is the
// same bytes. A tag (only under --allow-unverified) is re-pulled so a
// node never runs a stale or swapped cached image under that tag.
func updaterPullPolicy(image string) corev1.PullPolicy {
	if strings.Contains(image, "@sha256:") {
		return corev1.PullIfNotPresent
	}
	return corev1.PullAlways
}
