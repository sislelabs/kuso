package updater

import (
	"context"
	"os"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

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
// runs as the kuso-server ServiceAccount (cluster-admin in our
// install — the upgrade walks CRDs and rolls deployments). For
// hardened deployments we'd cut a dedicated kuso-updater SA with
// just the verbs the updater script uses; logged here as a
// follow-up.
func (s *Service) applyJob(ctx context.Context, j *batchv1Job) error {
	bf := int32(0)
	one := int32(1)
	ttl := int32(86400)
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
					Containers: []corev1.Container{{
						Name:            "updater",
						Image:           j.Image,
						ImagePullPolicy: corev1.PullIfNotPresent,
						Env:             env,
					}},
				},
			},
		},
	}
	_, err := s.Kube.Clientset.BatchV1().Jobs(j.Namespace).Create(ctx, job, metav1.CreateOptions{})
	return err
}

// getenv is the package-local fallback for envOrDefault. We don't
// import os in updater.go because the file has enough imports
// already; this lets it stay readable.
func getenv(key string) string {
	return os.Getenv(key)
}
