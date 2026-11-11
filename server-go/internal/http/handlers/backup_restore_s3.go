package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/audit"
)

// A control-plane dump up to secretRestoreMax rides to the restore Job in a
// Secret (Secret data caps out near 1 MiB). Anything larger, which is any
// install with logs, is staged in the backup bucket and the Job pulls it
// from there, the way addon restores already work.
const (
	secretRestoreMax = 900 * 1024
	// MaxControlPlaneRestoreBytes caps the restore upload. The router lifts
	// its global body cap to this for /api/admin/restore only.
	MaxControlPlaneRestoreBytes = 4 << 30
	// restoreUploadWindow replaces the server-wide 30s ReadTimeout for the
	// restore body, which a multi-GB upload cannot meet.
	restoreUploadWindow  = 30 * time.Minute
	restoreKeyAnnotation = "kuso.sislelabs.com/restore-key"
	restoreStagePrefix   = "kuso-controlplane/restore/"
)

// restoreViaS3 stages a large dump (head is what Upload already read) in the
// backup bucket and starts a Job that downloads it, checks its sha256 and
// applies it. Responds exactly like the Secret path.
func (h *BackupHandler) restoreViaS3(w http.ResponseWriter, r *http.Request, head []byte, src io.Reader) {
	ctx, cancel := context.WithTimeout(r.Context(), restoreUploadWindow)
	defer cancel()
	bh := &BackupsHandler{Kube: h.Kube, Namespace: h.Namespace}
	cli, bucket, err := bh.s3Client(ctx)
	if err != nil || bucket == "" {
		writeErr(w, http.StatusRequestEntityTooLarge,
			"dump exceeds 900 KiB, so the restore stages it in backup storage, which is not configured; set backup settings first, or run pg_restore directly against kuso-postgres (see docs/BACKUP_RESTORE.md)")
		return
	}

	f, err := os.CreateTemp("", "kuso-restore-*.sql.gz")
	if err != nil {
		h.Logger.Error("restore: temp file", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	defer os.Remove(f.Name())
	defer f.Close()
	sum := sha256.New()
	mw := io.MultiWriter(f, sum)
	if _, err := mw.Write(head); err != nil {
		writeErr(w, http.StatusInternalServerError, "stage dump: "+err.Error())
		return
	}
	rest, err := io.Copy(mw, io.LimitReader(src, MaxControlPlaneRestoreBytes-int64(len(head))+1))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	size := int64(len(head)) + rest
	if size > MaxControlPlaneRestoreBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("dump exceeds %d GiB", MaxControlPlaneRestoreBytes>>30))
		return
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		writeErr(w, http.StatusInternalServerError, "stage dump: "+err.Error())
		return
	}
	digest := hex.EncodeToString(sum.Sum(nil))

	stamp := time.Now().UTC().Format("20060102t150405z")
	jobName := "kuso-restore-" + stamp
	key := restoreStagePrefix + stamp + ".sql.gz"
	if _, err := cli.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(bucket),
		Key:           aws.String(key),
		Body:          f,
		ContentLength: aws.Int64(size),
	}); err != nil {
		h.Logger.Error("restore: stage dump in backup bucket", "err", err)
		writeErr(w, http.StatusBadGateway, "stage dump in backup storage: "+err.Error())
		return
	}
	if _, err := h.Kube.Clientset.BatchV1().Jobs(h.Namespace).Create(ctx, h.s3RestoreJob(jobName, key, digest), metav1.CreateOptions{}); err != nil {
		h.deleteStagedDump(key)
		h.Logger.Error("restore: create job", "err", err)
		writeErr(w, http.StatusInternalServerError, "create job: "+err.Error())
		return
	}
	h.Audit.Log(ctx, audit.Entry{
		User:     auditUser(ctx),
		Severity: "critical",
		Action:   "controlplane.restore",
		Resource: "kuso-postgres",
		Message:  fmt.Sprintf("control-plane restore started (job %s, %d bytes, staged at %s, sha256 %s)", jobName, size, key, digest),
	})
	writeJSON(w, http.StatusAccepted, map[string]string{
		"jobName":   jobName,
		"statusUrl": "/api/admin/restore/" + jobName,
		"hint":      "poll statusUrl until phase=Succeeded; the kuso-server rollout fires automatically on first success",
	})
}

// deleteStagedDump removes a staged dump once its Job is terminal.
// Best-effort: a leftover object is a stray copy of the control-plane DB in
// the backup bucket, which is where its backups live anyway.
func (h *BackupHandler) deleteStagedDump(key string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cli, bucket, err := (&BackupsHandler{Kube: h.Kube, Namespace: h.Namespace}).s3Client(ctx)
	if err == nil {
		_, err = cli.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	}
	if err != nil && h.Logger != nil {
		h.Logger.Warn("restore: delete staged dump", "key", key, "err", err)
	}
}

// controlPlaneApplyShell defines apply_dump, which both restore paths use.
//
// The whole dump goes in one transaction so a failure (or a deadlock with a
// still-running replica) rolls back to the old database instead of leaving
// tables dropped and recreated without their keys. It runs against the
// primary, not kuso-pgbouncer: in transaction mode each statement could land
// on a different backend, so the dump's session SETs (statement_timeout = 0)
// would not stick and the role's 60s timeout would cut every COPY short.
// Live replicas block on the dropped tables until COMMIT, then the rollout
// replaces them. A failed attempt left nothing behind, so it is retried.
// psql commits at end of input even when the input stopped mid-COPY, so a
// truncated dump is refused up front by its missing trailer.
// The GUC strip mirrors the addon restore: a newer pg_dump writes session
// settings an older server rejects under ON_ERROR_STOP.
const controlPlaneApplyShell = `
direct_dsn() {
  printf '%s' "$1" | sed -E 's#@kuso-pgbouncer((\.[A-Za-z0-9.-]+)?)(:[0-9]+)?/#@kuso-postgres-rw\1:5432/#'
}
apply_dump() { # gz file
  if ! gunzip -c "$1" | tail -n 20 | grep -q '^-- PostgreSQL database dump complete'; then
    echo "[restore] dump is truncated or is not a pg_dump plain dump (no 'dump complete' trailer); not touching the database" >&2
    exit 1
  fi
  _dsn=$(direct_dsn "${DSN}")
  # A dump without DROP statements (the scheduled CronJob's before it took
  # --clean) cannot load over the live schema, which boot always creates:
  # the first CREATE TABLE fails. Empty the public schema first, inside
  # the same transaction, so it rolls back with everything else.
  _wipe=""
  # grep -c, not -q: -q exits early and pipefail would count the SIGPIPE.
  if [ "$(gunzip -c "$1" | grep -cE '^DROP (TABLE|SCHEMA|EXTENSION) ' || true)" = "0" ]; then
    echo "[restore] dump has no DROP statements; replacing the public schema in the same transaction"
    _wipe="DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public;"
  fi
  _n=1
  until { echo "${_wipe}"; gunzip -c "$1"; } \
    | grep -vxE "SET (transaction_timeout|idle_session_timeout) = '?[^']*'?;" \
    | psql "${_dsn}" -q -v ON_ERROR_STOP=1 --single-transaction; do
    if [ "${_n}" -ge 3 ]; then
      echo "[restore] failed ${_n} times; every attempt was rolled back, the database is unchanged" >&2
      exit 1
    fi
    echo "[restore] attempt ${_n} rolled back; retrying in 10s" >&2
    _n=$((_n+1))
    sleep 10
  done
}
`

// secretRestoreScript applies a dump small enough to ride in a Secret.
const secretRestoreScript = `
set -eo pipefail
` + controlPlaneApplyShell + `echo "[restore] starting at $(date -u)"
apply_dump /data/dump.sql.gz
echo "[restore] done at $(date -u)"
`

// s3RestoreScript downloads the staged dump and refuses it unless the
// sha256 matches what the server received.
const s3RestoreScript = `
set -eo pipefail
` + s3FetchShellCP + controlPlaneApplyShell + `echo "[restore] starting at $(date -u)"
s3_fetch "s3://${BUCKET}/${KEY}" /tmp/dump.sql.gz
GOT=$(sha256sum /tmp/dump.sql.gz | awk '{print $1}')
if [ "${GOT}" != "${EXPECT_SHA256}" ]; then
  echo "[restore] checksum MISMATCH: uploaded=${EXPECT_SHA256} downloaded=${GOT}; not touching the database" >&2
  exit 1
fi
apply_dump /tmp/dump.sql.gz
echo "[restore] done at $(date -u)"
`

const s3FetchShellCP = `
s3_fetch() { # src dst
  _i=0
  until aws s3 cp --endpoint-url "${S3_ENDPOINT}" "$1" "$2"; do
    _i=$((_i+1))
    if [ "${_i}" -ge 30 ]; then echo "[restore] could not download $1 after 60s" >&2; exit 1; fi
    sleep 2
  done
}
`

func (h *BackupHandler) s3RestoreJob(jobName, key, digest string) *batchv1.Job {
	zero := int32(0)
	one := int32(1)
	ttl := int32(7 * 24 * 3600)
	env := append([]corev1.EnvVar{
		{Name: "KEY", Value: key},
		{Name: "EXPECT_SHA256", Value: digest},
		envFromSecret("DSN", "kuso-postgres-conn", "dsn"),
	}, restoreS3Env("")...)
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:        jobName,
			Namespace:   h.Namespace,
			Labels:      map[string]string{"app.kubernetes.io/name": "kuso-restore", "app.kubernetes.io/component": "job"},
			Annotations: map[string]string{restoreKeyAnnotation: key},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &zero,
			Completions:             &one,
			Parallelism:             &one,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{
					"app.kubernetes.io/name":      "kuso-restore",
					"app.kubernetes.io/component": "psql",
					// Same egress label the backup CronJobs carry, to reach S3.
					"kuso.sislelabs.com/network-egress-public": "true",
				}},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:            "restore",
						Image:           "ghcr.io/sislelabs/kuso-backup:latest",
						ImagePullPolicy: corev1.PullAlways,
						Command:         []string{"sh", "-c"},
						Args:            []string{s3RestoreScript},
						Env:             env,
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: ptrBool(false),
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
						VolumeMounts: []corev1.VolumeMount{{Name: "tmp", MountPath: "/tmp"}},
					}},
					Volumes: []corev1.Volume{{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
				},
			},
		},
	}
}
