package main

import (
	"context"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	// homeNS is the namespace SetSettings writes the cluster-wide
	// kuso-backup-s3 Secret into. When a project runs in its own execution
	// namespace we mirror the secret from here into that namespace so the
	// snapshot Job can resolve its BUCKET/S3 env refs (same gap Restore
	// handles via mirrorBackupSecret).
	homeNS string
}

func newSnapshotAdapter(kc *kube.Client, ns string) *snapshotAdapter {
	return &snapshotAdapter{kc: kc, addons: addons.New(kc, ns), homeNS: ns}
}

// snapshotPollInterval / snapshotPollTimeout bound the wait for the snapshot
// Job to complete. The migration MUST NOT start until the snapshot is a
// verified-good restore point, so CreateSnapshotJob blocks here.
var (
	snapshotPollInterval = 3 * time.Second
	snapshotPollTimeout  = 5 * time.Minute
)

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

	// The snapshot Job sources BUCKET/S3_ENDPOINT/AWS creds from the
	// cluster-wide kuso-backup-s3 Secret, which SetSettings only ever writes
	// into the home namespace. For a project with a per-project execution
	// namespace, a Job in `ns` referencing that secret would resolve BUCKET to
	// empty (the env refs are Optional) and the script would exit 1 ("backup
	// S3 not configured"). Mirror the secret into `ns` first — same fix Restore
	// applies — so the snapshot can actually run. Best-effort: if the source
	// secret is missing the Job's own BUCKET guard fails loudly and the
	// completion-poll below surfaces that failure rather than stamping a bad
	// restore point.
	if ns != a.homeNS {
		if err := a.mirrorBackupSecret(ctx, ns); err != nil {
			return "", fmt.Errorf("mirror backup secret into %s: %w", ns, err)
		}
	}

	script := fmt.Sprintf(`
set -eo pipefail
if [ -z "${BUCKET:-}" ]; then
  echo "==> backup S3 not configured — cannot snapshot" >&2
  exit 1
fi
echo "==> pre-deploy snapshot %s → s3://${BUCKET}/${KEY}"
PGPASSWORD="${POSTGRES_PASSWORD}" pg_dump --clean --if-exists -h "${POSTGRES_HOST}" -U "${POSTGRES_USER}" "${POSTGRES_DB}" | gzip > /tmp/dump.gz
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
	// Block until the snapshot Job actually finishes. Returning the key the
	// instant the Job is created is fire-and-forget: the caller (the build
	// poller's Snapshotter path) would let the migration run against data the
	// snapshot hadn't captured yet — or against a snapshot that FAILED (empty
	// BUCKET, pg_dump error) that we'd have silently recorded as a good
	// restore point. Poll to a terminal condition and propagate failure so the
	// migration is gated on a real, completed snapshot.
	if err := a.waitForJob(ctx, ns, jobName); err != nil {
		return "", err
	}
	return key, nil
}

// waitForJob polls a Job until it reports a terminal condition (Complete or
// Failed) or the bounded timeout elapses. Returns nil only on Complete; a
// Failed Job, a timeout, or a context cancellation is an error so the caller
// treats the snapshot as NOT taken.
func (a *snapshotAdapter) waitForJob(ctx context.Context, ns, jobName string) error {
	deadline := time.Now().Add(snapshotPollTimeout)
	for {
		job, err := a.kc.Clientset.BatchV1().Jobs(ns).Get(ctx, jobName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("poll snapshot job %s/%s: %w", ns, jobName, err)
		}
		switch snapshotJobPhase(job) {
		case jobComplete:
			return nil
		case jobFailed:
			return fmt.Errorf("snapshot job %s/%s failed", ns, jobName)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("snapshot job %s/%s did not complete within %s", ns, jobName, snapshotPollTimeout)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("snapshot job %s/%s wait cancelled: %w", ns, jobName, ctx.Err())
		case <-time.After(snapshotPollInterval):
		}
	}
}

type jobPhase int

const (
	jobRunning jobPhase = iota
	jobComplete
	jobFailed
)

// snapshotJobPhase reduces a Job's conditions to a terminal phase. A Job with
// a Complete condition set to True is done; a Failed condition set to True
// (BackoffLimit=0 → first pod failure is terminal) is a hard failure.
func snapshotJobPhase(job *batchv1.Job) jobPhase {
	for _, c := range job.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete:
			return jobComplete
		case batchv1.JobFailed:
			return jobFailed
		}
	}
	return jobRunning
}

// mirrorBackupSecret copies the home-namespace kuso-backup-s3 Secret into a
// target (project execution) namespace so a snapshot Job running there can
// resolve its BUCKET/S3_ENDPOINT/AWS-credential env refs. Mirrors
// handlers.mirrorBackupSecret; kept minimal here to avoid a cross-package
// dependency from cmd into the handlers package. Idempotent: updates the
// mirror's data in place when it already exists.
func (a *snapshotAdapter) mirrorBackupSecret(ctx context.Context, ns string) error {
	secrets := a.kc.Clientset.CoreV1()
	src, err := secrets.Secrets(a.homeNS).Get(ctx, snapshotBackupSecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("%s not found in %s (backups not configured)", snapshotBackupSecretName, a.homeNS)
	}
	if err != nil {
		return fmt.Errorf("read source secret: %w", err)
	}
	data := make(map[string][]byte, len(src.Data))
	for k, v := range src.Data {
		data[k] = v
	}
	dstSecrets := secrets.Secrets(ns)
	if existing, gerr := dstSecrets.Get(ctx, snapshotBackupSecretName, metav1.GetOptions{}); gerr == nil {
		existing.Data = data
		if _, uerr := dstSecrets.Update(ctx, existing, metav1.UpdateOptions{}); uerr != nil {
			return fmt.Errorf("update mirrored secret: %w", uerr)
		}
		return nil
	} else if !apierrors.IsNotFound(gerr) {
		return fmt.Errorf("check mirrored secret: %w", gerr)
	}
	dst := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      snapshotBackupSecretName,
			Namespace: ns,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "kuso-server",
				"kuso.sislelabs.com/mirror-of": a.homeNS,
			},
		},
		Type: src.Type,
		Data: data,
	}
	if _, cerr := dstSecrets.Create(ctx, dst, metav1.CreateOptions{}); cerr != nil && !apierrors.IsAlreadyExists(cerr) {
		return fmt.Errorf("create mirrored secret: %w", cerr)
	}
	return nil
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
