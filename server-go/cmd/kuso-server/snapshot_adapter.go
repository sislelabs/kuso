package main

import (
	"context"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/addons"
	"kuso/server/internal/kube"
)

// snapshotBackupSecretName mirrors handlers.backupSecretName — the
// cluster-wide S3 credentials Secret the snapshot Job reads.
const snapshotBackupSecretName = "kuso-backup-s3"

// snapshotAdapter implements builds.AddonKindLister + builds.SnapshotJobCreator.
// It resolves an addon's kind via the addons service and creates a postgres
// backup Job (pg_dump | gzip | sha256 + manifest → S3) for a pre-deploy
// snapshot — the same artifact shape scheduled backups write, so it appears
// in the existing backup list and restores through the verified path.
type snapshotAdapter struct {
	kc     *kube.Client
	addons *addons.Service
}

func newSnapshotAdapter(kc *kube.Client, ns string) *snapshotAdapter {
	return &snapshotAdapter{kc: kc, addons: addons.New(kc, ns)}
}

// AddonKind returns the addon's kind, ownership-checked to the project.
func (a *snapshotAdapter) AddonKind(ctx context.Context, project, addon string) (string, error) {
	cr, err := a.addons.GetOwned(ctx, project, addon)
	if err != nil {
		return "", err
	}
	return cr.Spec.Kind, nil
}

// CreateSnapshotJob creates a one-shot postgres backup Job and returns the
// S3 key it will write. Mirrors the postgres backup CronJob's script
// (kusoaddon/templates/backup-cronjob.yaml): temp-file dump → sha256 →
// upload artifact + manifest. trigger/buildRef are recorded in the manifest
// for provenance.
func (a *snapshotAdapter) CreateSnapshotJob(ctx context.Context, project, addon, trigger, buildRef string) (string, error) {
	ns := a.addons.NamespaceFor(ctx, project)
	release := addons.CRName(project, addon)
	ts := time.Now().UTC().Format("20060102T150405Z")
	key := fmt.Sprintf("%s/%s/%s.sql.gz", project, release, ts)

	script := fmt.Sprintf(`
set -eo pipefail
if [ -z "${BUCKET:-}" ]; then
  echo "==> backup S3 not configured — cannot snapshot" >&2
  exit 1
fi
echo "==> pre-deploy snapshot %s → s3://${BUCKET}/${KEY}"
PGPASSWORD="${POSTGRES_PASSWORD}" pg_dump -h "${POSTGRES_HOST}" -U "${POSTGRES_USER}" "${POSTGRES_DB}" | gzip > /tmp/dump.gz
SHA=$(sha256sum /tmp/dump.gz | awk '{print $1}')
BYTES=$(wc -c < /tmp/dump.gz | tr -d ' ')
aws s3 cp --endpoint-url "${S3_ENDPOINT}" /tmp/dump.gz "s3://${BUCKET}/${KEY}"
printf '{"schemaVersion":1,"createdAt":"%%s","project":"%s","addon":"%s","addonKind":"postgres","producer":"pg_dump","trigger":"%s","buildRef":"%s","artifacts":[{"key":"%%s","sha256":"%%s","bytes":%%s,"payloadKind":"pg_dump"}]}\n' \
  "$(date -u +%%Y-%%m-%%dT%%H:%%M:%%SZ)" "${KEY}" "${SHA}" "${BYTES}" > /tmp/manifest.json
aws s3 cp --endpoint-url "${S3_ENDPOINT}" /tmp/manifest.json "s3://${BUCKET}/${KEY}.manifest.json"
echo "==> snapshot done (sha256=${SHA})"
`, addon, project, addon, trigger, buildRef)

	one := int32(1)
	zero := int32(0)
	jobName := fmt.Sprintf("%s-snapshot-%d", release, time.Now().Unix())
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: ns,
			Labels: map[string]string{
				"kuso.sislelabs.com/role":    "snapshot",
				"kuso.sislelabs.com/project": project,
				"kuso.sislelabs.com/addon":   addon,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &zero,
			Completions:  &one,
			Parallelism:  &one,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:            "snapshot",
						Image:           "ghcr.io/sislelabs/kuso-backup:latest",
						ImagePullPolicy: corev1.PullIfNotPresent,
						Command:         []string{"sh", "-c"},
						Args:            []string{script},
						Env: []corev1.EnvVar{
							{Name: "KEY", Value: key},
							snapEnvFromSecret("POSTGRES_HOST", release+"-conn", "POSTGRES_HOST"),
							snapEnvFromSecret("POSTGRES_USER", release+"-conn", "POSTGRES_USER"),
							snapEnvFromSecret("POSTGRES_DB", release+"-conn", "POSTGRES_DB"),
							snapEnvFromSecret("POSTGRES_PASSWORD", release+"-conn", "POSTGRES_PASSWORD"),
							snapEnvFromSecretOpt("BUCKET", snapshotBackupSecretName, "bucket"),
							snapEnvFromSecretOpt("S3_ENDPOINT", snapshotBackupSecretName, "endpoint"),
							snapEnvFromSecretOpt("AWS_ACCESS_KEY_ID", snapshotBackupSecretName, "accessKeyId"),
							snapEnvFromSecretOpt("AWS_SECRET_ACCESS_KEY", snapshotBackupSecretName, "secretAccessKey"),
							snapEnvFromSecretOpt("AWS_DEFAULT_REGION", snapshotBackupSecretName, "region"),
						},
					}},
				},
			},
		},
	}
	if _, err := a.kc.Clientset.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{}); err != nil {
		return "", fmt.Errorf("create snapshot job: %w", err)
	}
	return key, nil
}

func snapEnvFromSecret(name, secretName, key string) corev1.EnvVar {
	return corev1.EnvVar{
		Name: name,
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
				Key:                  key,
			},
		},
	}
}

func snapEnvFromSecretOpt(name, secretName, key string) corev1.EnvVar {
	opt := true
	e := snapEnvFromSecret(name, secretName, key)
	e.ValueFrom.SecretKeyRef.Optional = &opt
	return e
}
