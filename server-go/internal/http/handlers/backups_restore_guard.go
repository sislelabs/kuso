package handlers

import (
	"context"
	"fmt"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/backup"
	"kuso/server/internal/kube"
)

// labelRestoreDest carries the destination addon's CR name on a restore
// Job. The older kuso.sislelabs.com/addon label holds whatever alias the
// caller typed (short or FQN), so it cannot be matched reliably.
const labelRestoreDest = "kuso.sislelabs.com/restore-dest"

// restoreLockTimeout bounds how long a postgres restore waits on a lock an
// app session holds. Without it --single-transaction queued behind the app
// for the Job's whole 6h deadline; with it the restore fails and rolls back.
const restoreLockTimeout = "60s"

// snapshotWait is how long a restore waits for its pre-restore snapshot to
// land in the bucket before giving up without touching the database.
const snapshotWait = 2 * time.Hour

// jobFinished reports whether a Job reached a terminal condition.
func jobFinished(j *batchv1.Job) bool {
	for _, c := range j.Status.Conditions {
		if (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return j.Status.CompletionTime != nil
}

// activeRestore returns the name of a restore Job still running against the
// destination addon, "" when there is none. Two restores into one database
// interleave their DROP/CREATE and the last COMMIT wins, so the second is
// refused instead.
func (h *BackupsHandler) activeRestore(ctx context.Context, ns, destCR, destAlias string) (string, error) {
	jobs, err := h.Kube.Clientset.BatchV1().Jobs(ns).List(ctx, metav1.ListOptions{LabelSelector: "kuso.sislelabs.com/role=restore"})
	if err != nil {
		return "", fmt.Errorf("list restore jobs: %w", err)
	}
	for i := range jobs.Items {
		j := &jobs.Items[i]
		if jobFinished(j) || j.DeletionTimestamp != nil {
			continue
		}
		l := j.Labels
		if l[labelRestoreDest] == destCR || (l[labelRestoreDest] == "" && (l["kuso.sislelabs.com/addon"] == destCR || l["kuso.sislelabs.com/addon"] == destAlias)) {
			return j.Name, nil
		}
	}
	return "", nil
}

// restoreBackupSecret picks the backup-storage Secret a restore Job in ns
// reads: the project's own kuso-backup-s3-<project> (a project-scoped key
// when one is set, BLOB-4) when kuso-server has rendered it there, else the
// shared kuso-backup-s3. An instance tenant's dumps live under the instance
// addon's prefix, outside what a project-scoped key may read, so those keep
// the shared Secret.
func (h *BackupsHandler) restoreBackupSecret(ctx context.Context, ns, project string, instanceTenant bool) (string, error) {
	if instanceTenant {
		return backupSecretName, nil
	}
	name := kube.ProjectBackupSecretName(project)
	_, err := h.Kube.Clientset.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return name, nil
	}
	if apierrors.IsNotFound(err) {
		return backupSecretName, nil
	}
	return "", fmt.Errorf("read %s/%s: %w", ns, name, err)
}

// snapshotKinds are the kinds whose backup CronJob writes a single dump per
// run, so a restore can wait for that run's manifest.
var snapshotKinds = map[string]bool{"postgres": true, "mysql": true, "mongodb": true}

// startPreRestoreSnapshot runs the destination's scheduled backup now, so
// an in-place restore that turns out wrong can be undone. It returns the
// snapshot Job and the time its dump is stamped after, or a warning when
// there is nothing to snapshot with (no schedule, an instance tenant,
// another kind); the restore then proceeds as it always did.
func (h *BackupsHandler) startPreRestoreSnapshot(ctx context.Context, ns string, dest *kube.KusoAddon) (*batchv1.Job, time.Time, string, error) {
	if !snapshotKinds[dest.Spec.Kind] {
		return nil, time.Time{}, fmt.Sprintf("no pre-restore snapshot: %s addons are not snapshotted", dest.Spec.Kind), nil
	}
	if dest.Spec.UseInstanceAddon != "" {
		return nil, time.Time{}, "no pre-restore snapshot: instance-shared databases are backed up by the instance addon's schedule", nil
	}
	cj, err := h.Kube.Clientset.BatchV1().CronJobs(ns).Get(ctx, dest.Name+"-backup", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, time.Time{}, "no pre-restore snapshot: this addon has no backup schedule, so the current data is not saved before the restore", nil
	}
	if err != nil {
		return nil, time.Time{}, "", fmt.Errorf("get backup cronjob: %w", err)
	}
	// Second-granularity backup stamps: the snapshot's TS is taken inside
	// its pod, strictly after this.
	since := time.Now().UTC().Truncate(time.Second)
	job := manualJobFromCronJob(cj, since)
	suffix := fmt.Sprintf("-pre-%d", since.Unix())
	base := cj.Name
	if len(base)+len(suffix) > 63 {
		base = strings.TrimRight(base[:63-len(suffix)], "-.")
	}
	job.Name = base + suffix
	created, err := h.Kube.Clientset.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		return nil, time.Time{}, "", fmt.Errorf("start pre-restore snapshot: %w", err)
	}
	return created, since, "", nil
}

// snapshotWaitShell blocks until the pre-restore snapshot's manifest for
// SNAP_SERIES (the name after the stamp, e.g. ".db-orders.sql.gz.manifest.json")
// stamped at or after SNAP_SINCE appears under SNAP_PREFIX. A postgres run
// uploads the primary dump first and each other database after it, so
// waiting for any manifest let a per-database restore start before that
// database's undo copy existed (BLOB-4). Empty SNAP_SERIES (a key with no
// stamp) waits for any manifest. When SNAP_DB names a database the server
// doesn't have yet there is nothing to save, so the primary series is
// enough; if that can't be checked, the gate keeps waiting for SNAP_DB's
// own dump. If the manifest never appears, the init container fails and
// the restore never touches the database.
const snapshotWaitShell = `
set -eu
if [ -n "${SNAP_DB:-}" ]; then
  _i=0
  until _have=$(printf '%s\n' "SELECT 1 FROM pg_database WHERE datname = :'db'" \
      | PGPASSWORD="${POSTGRES_PASSWORD}" psql -h "${POSTGRES_HOST}" -U "${POSTGRES_USER}" -d "${POSTGRES_DB}" -v db="${SNAP_DB}" -tA 2>/dev/null); do
    _i=$((_i+1))
    if [ "${_i}" -ge 30 ]; then _have=unknown; break; fi
    sleep 2
  done
  if [ -z "${_have}" ]; then
    echo "==> database ${SNAP_DB} does not exist yet: nothing to save beyond the primary dump"
    SNAP_SERIES="${SNAP_PRIMARY_SERIES}"
  fi
fi
_end=$(( $(date +%s) + SNAP_WAIT_SECONDS ))
echo "==> waiting for the pre-restore snapshot under s3://${BUCKET}/${SNAP_PREFIX} (stamped >= ${SNAP_SINCE}, series ${SNAP_SERIES:-any})"
while :; do
  _hit=$(aws s3 ls --endpoint-url "${S3_ENDPOINT}" "s3://${BUCKET}/${SNAP_PREFIX}" 2>/dev/null \
    | awk -v s="${SNAP_SINCE}" -v series="${SNAP_SERIES:-}" '$4 ~ /\.manifest\.json$/ && substr($4, 1, 16) >= s && (series == "" || substr($4, 17) == series) { print $4; exit }' || true)
  if [ -n "${_hit}" ]; then echo "==> pre-restore snapshot saved: ${SNAP_PREFIX}${_hit}"; exit 0; fi
  if [ "$(date +%s)" -ge "${_end}" ]; then
    echo "==> pre-restore snapshot did not complete in time; the database was NOT touched. Check the snapshot Job, or retry with skipSnapshot (CLI --skip-snapshot)." >&2
    exit 1
  fi
  sleep 10
done
`

// snapshotSeries returns the manifest name, minus its stamp, that the
// pre-restore snapshot writes for the data a restore of key overwrites:
// the same series as key ("" when key carries no stamp).
func snapshotSeries(key string) string {
	base := key[strings.LastIndex(key, "/")+1:]
	if m := backupStampRe.FindString(base); m == "" || m == base {
		return ""
	}
	return base[len(backupStampLayout):] + ".manifest.json"
}

// snapshotWaitContainer is the restore Job's init container gating the
// restore on its snapshot. series is snapshotSeries of the restored key;
// siblingDB, when set, is the postgres database a per-database dump
// restores into, and connSecret the destination's conn Secret used to
// check whether that database exists yet.
func snapshotWaitContainer(s3Secret, bucketOverride, prefix string, since time.Time, series, siblingDB, connSecret string) corev1.Container {
	env := append([]corev1.EnvVar{
		{Name: "SNAP_PREFIX", Value: prefix},
		{Name: "SNAP_SINCE", Value: since.UTC().Format(backupStampLayout)},
		{Name: "SNAP_SERIES", Value: series},
		{Name: "SNAP_WAIT_SECONDS", Value: fmt.Sprintf("%d", int(snapshotWait.Seconds()))},
	}, restoreS3EnvFrom(s3Secret, bucketOverride)...)
	if siblingDB != "" {
		env = append(env,
			corev1.EnvVar{Name: "SNAP_DB", Value: siblingDB},
			corev1.EnvVar{Name: "SNAP_PRIMARY_SERIES", Value: ".sql.gz.manifest.json"},
			envFromSecret("POSTGRES_HOST", connSecret, "POSTGRES_HOST"),
			envFromSecret("POSTGRES_USER", connSecret, "POSTGRES_USER"),
			envFromSecret("POSTGRES_DB", connSecret, "POSTGRES_DB"),
			envFromSecret("POSTGRES_PASSWORD", connSecret, "POSTGRES_PASSWORD"),
		)
	}
	return corev1.Container{
		Name:            "wait-snapshot",
		Image:           backupJobImage,
		ImagePullPolicy: corev1.PullAlways,
		Command:         []string{"sh", "-c"},
		Args:            []string{snapshotWaitShell},
		Env:             env,
	}
}

// backupStampLayout is the YYYYMMDDTHHMMSSZ stamp every backup key starts with.
const backupStampLayout = "20060102T150405Z"

// backupJobImage runs every backup, restore and drill Job.
const backupJobImage = "ghcr.io/sislelabs/kuso-backup:latest"

// createRestoreKeyCopy writes the copied backup key Secret owned by the
// restore Job from the start, so it is garbage-collected with the Job even
// if kuso-server dies right after (DB-9). The Job's env refers to it
// non-optionally, so the pod waits for it rather than starting keyless.
func (h *BackupsHandler) createRestoreKeyCopy(ctx context.Context, job *batchv1.Job, project, val string) error {
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: job.Name + "-key", Namespace: job.Namespace,
			Labels: map[string]string{kube.LabelProject: project},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID,
			}},
		},
		Data: map[string][]byte{backup.KeySecretKey: []byte(val)},
	}
	_, err := h.Kube.Clientset.CoreV1().Secrets(job.Namespace).Create(ctx, sec, metav1.CreateOptions{})
	return err
}
