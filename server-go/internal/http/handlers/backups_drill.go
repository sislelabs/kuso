package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/addons"
	"kuso/server/internal/audit"
	"kuso/server/internal/db"
	"kuso/server/internal/kube"
)

// A restore drill proves a stored backup actually restores: it replays the
// dump through the exact restore script into a throwaway database on the
// addon's own server, counts what came back, and drops the database. A
// green backup Job only proves pg_dump exited 0 (DB-11).

const drillRole = "restore-drill"

// DrillRequest picks the backup to drill; empty Key = the newest one.
type DrillRequest struct {
	Key             string `json:"key,omitempty"`
	AllowUnverified bool   `json:"allowUnverified,omitempty"`
}

// DrillStatus is the drill Job's outcome, parsed from its log.
type DrillStatus struct {
	Job    string `json:"job"`
	Key    string `json:"key,omitempty"`
	Phase  string `json:"phase"` // Running | Succeeded | Failed
	Tables *int64 `json:"tables,omitempty"`
	Rows   *int64 `json:"rows,omitempty"`
	// Message is the drill's own explanation of a refusal or failure.
	Message string `json:"message,omitempty"`
}

// drillScriptPrelude creates the scratch database, refusing up front when
// the conn role cannot, or when restoring a copy could fill the volume the
// live database sits on. drillScriptEpilogue reports what was restored.
// The scratch database is dropped on every exit path.
const drillScriptPrelude = `
set -eo pipefail
pg_q() { _db=$1; shift; PGPASSWORD="${POSTGRES_PASSWORD}" psql -X -v ON_ERROR_STOP=1 -qAt -h "${POSTGRES_HOST}" -U "${POSTGRES_USER}" -d "${_db}" "$@"; }
_i=0
until PGPASSWORD="${POSTGRES_PASSWORD}" pg_isready -h "${POSTGRES_HOST}" -U "${POSTGRES_USER}" -q; do
  _i=$((_i+1))
  if [ "${_i}" -ge 30 ]; then echo "DRILL_ERROR: postgres ${POSTGRES_HOST} unreachable after 60s"; exit 1; fi
  sleep 2
done
LIVE_DB="${POSTGRES_DB}"
if [ "$(pg_q "${LIVE_DB}" -c "SELECT rolsuper OR rolcreatedb FROM pg_roles WHERE rolname = current_user")" != "t" ]; then
  echo "DRILL_UNSUPPORTED: role ${POSTGRES_USER} cannot CREATE DATABASE, so there is nowhere to restore a scratch copy"
  exit 2
fi
if [ "${DRILL_VOLUME_BYTES:-0}" -gt 0 ]; then
  FITS=$(pg_q "${LIVE_DB}" -c "SELECT sum(pg_database_size(oid)) + 1.2 * COALESCE((SELECT pg_database_size(datname) FROM pg_database WHERE datname = '${DRILL_SIZE_DB}'), 0) <= 0.85 * ${DRILL_VOLUME_BYTES} FROM pg_database")
  if [ "${FITS}" != "t" ]; then
    echo "DRILL_UNSUPPORTED: a scratch copy of ${DRILL_SIZE_DB} would push the addon volume past 85%; grow storageSize or drill into a sibling addon with 'addon-backup restore --into'"
    exit 2
  fi
fi
drill_cleanup() {
  pg_q "${LIVE_DB}" -c "DROP DATABASE IF EXISTS \"${DRILL_DB}\" WITH (FORCE)" >/dev/null 2>&1 \
    || pg_q "${LIVE_DB}" -c "DROP DATABASE IF EXISTS \"${DRILL_DB}\"" >/dev/null 2>&1 \
    || echo "==> WARNING: could not drop scratch database ${DRILL_DB}; drop it by hand" >&2
}
trap drill_cleanup EXIT
pg_q "${LIVE_DB}" -c "CREATE DATABASE \"${DRILL_DB}\""
echo "==> restoring ${KEY} into scratch database ${DRILL_DB}"
`

const drillScriptEpilogue = `
STATS=$(pg_q "${DRILL_DB}" -c "ANALYZE" -c "SELECT count(*), COALESCE(sum(GREATEST(c.reltuples, 0)), 0)::bigint FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE c.relkind IN ('r', 'p') AND n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg_toast%'")
echo "DRILL_RESULT tables=${STATS%%|*} rows=${STATS##*|}"
`

// drillScript wraps the production postgres restore script, run unchanged
// in a subshell against the scratch database, so the drill exercises the
// same download, decrypt, manifest check and psql pipeline a real restore
// uses.
func drillScript() (string, error) {
	restore, err := restoreScriptForKind("postgres")
	if err != nil {
		return "", err
	}
	return drillScriptPrelude + "(\nexport POSTGRES_DB=\"${DRILL_DB}\"\n" + restore + "\n)\n" + drillScriptEpilogue, nil
}

// addonVolumeBytes mirrors the chart's kusoaddon.storageSize default.
func addonVolumeBytes(a *kube.KusoAddon) int64 {
	size := a.Spec.StorageSize
	if size == "" {
		switch a.Spec.Size {
		case "medium":
			size = "20Gi"
		case "large":
			size = "100Gi"
		default:
			size = "5Gi"
		}
	}
	q, err := resource.ParseQuantity(size)
	if err != nil {
		return 0
	}
	return q.Value()
}

// newestPrimaryDump picks the newest primary pg_dump among keys (newest
// first), skipping per-database sibling dumps.
func newestPrimaryDump(items []BackupObject) string {
	for _, it := range items {
		base := it.Key[strings.LastIndex(it.Key, "/")+1:]
		if strings.HasSuffix(base, ".sql.gz") && !siblingDumpRe.MatchString(base) {
			return it.Key
		}
	}
	return ""
}

func (h *BackupsHandler) Drill(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	addon := chi.URLParam(r, "addon")
	var req DrillRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid body")
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	// Creates and drops a database on the addon's server: a write, like
	// restore and backup-run.
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleEditor) {
		return
	}
	cr, ns, err := h.ownedAddon(ctx, project, addon)
	if err != nil {
		writeAddonErr(w, err)
		return
	}
	switch {
	case cr.Spec.Kind != "postgres":
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("restore drills support postgres addons only (this is %s)", cr.Spec.Kind))
		return
	case isExternalAddon(cr):
		writeErr(w, http.StatusBadRequest, "restore drills do not create scratch databases on external providers; restore into a kuso addon with 'addon-backup restore --into' instead")
		return
	case cr.Spec.UseInstanceAddon != "":
		writeErr(w, http.StatusBadRequest, "restore drills are not supported for instance-shared databases (the tenant role cannot create a scratch database)")
		return
	}
	prefix := project + "/" + cr.Name + "/"
	if req.Key == "" {
		cli, bucket, err := h.s3Client(ctx)
		if err != nil {
			writeErr(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		bucket = cr.BackupBucket(bucket)
		own := h.addonBackupOwnership(ctx, cli, bucket, prefix, cr)
		items, _, err := listOwnedBackups(ctx, cli, bucket, prefix, math.MaxInt, func(o BackupObject) bool {
			return own.owns(backupObjectTime(prefix, o.Key, o.When))
		})
		if err != nil {
			writeErr(w, http.StatusBadGateway, "list backups: "+err.Error())
			return
		}
		if req.Key = newestPrimaryDump(items); req.Key == "" {
			writeErr(w, http.StatusNotFound, "no backups to drill for this addon")
			return
		}
	} else if !strings.HasPrefix(req.Key, prefix) || strings.Contains(req.Key, "..") || strings.ContainsRune(req.Key, '\x00') {
		writeErr(w, http.StatusBadRequest, "key must be one of this addon's backups")
		return
	}
	if code, msg := h.restoreKeyOwned(ctx, project, req.Key, nil); code != 0 {
		writeErr(w, code, msg)
		return
	}
	sizeDB, err := siblingDumpDB("postgres", req.Key)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if running, err := h.activeDrill(ctx, ns, cr.Name); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	} else if running != "" {
		writeErr(w, http.StatusConflict, fmt.Sprintf("restore drill %s is still running for %q", running, addon))
		return
	}
	s3Secret, err := h.restoreBackupSecret(ctx, ns, project, false)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if ns != h.Namespace && s3Secret == backupSecretName {
		if err := h.mirrorBackupSecret(ctx, ns); err != nil {
			if errors.Is(err, addons.ErrNotFound) {
				writeErr(w, http.StatusPreconditionFailed, "backups not configured: kuso-backup-s3 secret missing (set backup settings first)")
				return
			}
			writeErr(w, http.StatusInternalServerError, "internal")
			return
		}
	}
	script, err := drillScript()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	stamp := time.Now().Unix()
	jobName := fmt.Sprintf("%s-drill-%d", cr.Name, stamp)
	if len(jobName) > 63 {
		jobName = fmt.Sprintf("%s-drill-%d", strings.TrimRight(cr.Name[:63-len(fmt.Sprintf("-drill-%d", stamp))], "-."), stamp)
	}
	keyEnv, _, err := h.restoreKeyEnv(ctx, ns, jobName, project, req.Key, nil)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	env := append([]corev1.EnvVar{
		{Name: "KEY", Value: req.Key},
		{Name: "DRILL_DB", Value: fmt.Sprintf("kuso_drill_%d", stamp)},
		{Name: "DRILL_VOLUME_BYTES", Value: strconv.FormatInt(addonVolumeBytes(cr), 10)},
		keyEnv,
	}, restoreConnEnv("postgres", cr.Name, false)...)
	env = append(env, restoreS3EnvFrom(s3Secret, cr.BackupBucket(""))...)
	if sizeDB == "" {
		env = append(env, envFromSecret("DRILL_SIZE_DB", cr.Name+"-conn", "POSTGRES_DB"))
	} else {
		env = append(env, corev1.EnvVar{Name: "DRILL_SIZE_DB", Value: sizeDB})
	}
	if req.AllowUnverified {
		env = append(env, corev1.EnvVar{Name: "ALLOW_UNVERIFIED", Value: "1"})
	}
	job := buildRestoreJob(ns, jobName, project, cr.Name, cr.Name, script, env)
	job.Labels["kuso.sislelabs.com/role"] = drillRole
	job.Spec.Template.Labels["kuso.sislelabs.com/role"] = drillRole
	job.Annotations = map[string]string{"kuso.sislelabs.com/drill-key": req.Key}
	created, err := h.Kube.Clientset.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		h.Logger.Error("backup: create drill job", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	if h.Audit != nil {
		h.Audit.Log(ctx, audit.Entry{
			User: auditUser(ctx), Severity: "info", Action: "addon.restore_drill",
			Pipeline: project, App: addon, Resource: "kusoaddon",
			Message: fmt.Sprintf("restore drill of %s started job=%s", req.Key, created.Name),
		})
	}
	writeJSON(w, http.StatusCreated, DrillStatus{Job: created.Name, Key: req.Key, Phase: "Running"})
}

// activeDrill returns a drill Job still running for the addon.
func (h *BackupsHandler) activeDrill(ctx context.Context, ns, addonCR string) (string, error) {
	jobs, err := h.Kube.Clientset.BatchV1().Jobs(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "kuso.sislelabs.com/role=" + drillRole + ",kuso.sislelabs.com/addon=" + addonCR,
	})
	if err != nil {
		return "", fmt.Errorf("list drill jobs: %w", err)
	}
	for i := range jobs.Items {
		if !jobFinished(&jobs.Items[i]) {
			return jobs.Items[i].Name, nil
		}
	}
	return "", nil
}

func (h *BackupsHandler) DrillStatus(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	addon := chi.URLParam(r, "addon")
	jobName := chi.URLParam(r, "job")
	ctx, cancel := backupCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleViewer) {
		return
	}
	cr, ns, err := h.ownedAddon(ctx, project, addon)
	if err != nil {
		writeAddonErr(w, err)
		return
	}
	job, err := h.Kube.Clientset.BatchV1().Jobs(ns).Get(ctx, jobName, metav1.GetOptions{})
	if err != nil || job.Labels["kuso.sislelabs.com/role"] != drillRole || job.Labels["kuso.sislelabs.com/addon"] != cr.Name {
		writeErr(w, http.StatusNotFound, "drill job not found")
		return
	}
	st := DrillStatus{Job: job.Name, Key: job.Annotations["kuso.sislelabs.com/drill-key"], Phase: drillPhase(job)}
	if st.Phase != "Running" {
		parseDrillLog(h.drillLog(ctx, ns, job.Name), &st)
	}
	writeJSON(w, http.StatusOK, st)
}

func drillPhase(j *batchv1.Job) string {
	for _, c := range j.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete:
			return "Succeeded"
		case batchv1.JobFailed:
			return "Failed"
		}
	}
	return "Running"
}

// drillLog returns the tail of the drill pod's log, "" when unavailable.
func (h *BackupsHandler) drillLog(ctx context.Context, ns, job string) string {
	pods, err := h.Kube.Clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: "job-name=" + job})
	if err != nil || len(pods.Items) == 0 {
		return ""
	}
	tail := int64(200)
	raw, err := h.Kube.Clientset.CoreV1().Pods(ns).GetLogs(pods.Items[0].Name, &corev1.PodLogOptions{TailLines: &tail}).DoRaw(ctx)
	if err != nil {
		return ""
	}
	return string(raw)
}

var drillResultRe = regexp.MustCompile(`(?m)^DRILL_RESULT tables=(\d+) rows=(\d+)$`)
var drillMessageRe = regexp.MustCompile(`(?m)^(?:DRILL_UNSUPPORTED|DRILL_ERROR): (.*)$|^==> (.*(?:MISMATCH|refusing|unreachable|could not|NOT encrypted|no backup encryption key).*)$`)

func parseDrillLog(log string, st *DrillStatus) {
	if m := drillResultRe.FindStringSubmatch(log); m != nil {
		t, _ := strconv.ParseInt(m[1], 10, 64)
		n, _ := strconv.ParseInt(m[2], 10, 64)
		st.Tables, st.Rows = &t, &n
	}
	if m := drillMessageRe.FindStringSubmatch(log); m != nil {
		st.Message = m[1] + m[2]
	}
	if st.Phase == "Failed" && st.Message == "" {
		st.Message = "the drill failed; see the log of Job " + st.Job
	}
}
