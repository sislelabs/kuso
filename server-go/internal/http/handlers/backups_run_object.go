package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/go-chi/chi/v5"
	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/audit"
	"kuso/server/internal/db"
)

// RunBackup is POST /api/projects/{p}/addons/{a}/backups/run: an immediate
// run of the addon's scheduled backup, i.e. `kubectl create job
// --from=cronjob/<addon>-backup`. Editor, like Restore — it writes a dump
// to the configured bucket and returns no data to the caller.
func (h *BackupsHandler) RunBackup(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	addon := chi.URLParam(r, "addon")
	ctx, cancel := backupCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleEditor) {
		return
	}
	cr, ns, err := h.ownedAddon(ctx, project, addon)
	if err != nil {
		writeAddonErr(w, err)
		return
	}
	// The chart names the CronJob <fullname>-backup, fullname = release
	// name = CR name (kusoaddon/_helpers.tpl, backup-cronjob.yaml).
	cj, err := h.Kube.Clientset.BatchV1().CronJobs(ns).Get(ctx, cr.Name+"-backup", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		writeErr(w, http.StatusConflict, "no backup schedule is configured for this addon — turn on scheduled backups first")
		return
	}
	if err != nil {
		h.Logger.Error("backup: get backup cronjob", "err", err)
		writeErr(w, http.StatusBadGateway, "get backup schedule: "+err.Error())
		return
	}
	job := manualJobFromCronJob(cj, time.Now())
	created, err := h.Kube.Clientset.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		h.Logger.Error("backup: create manual backup job", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	if h.Audit != nil {
		h.Audit.Log(ctx, audit.Entry{
			User:     auditUser(ctx),
			Severity: "info",
			Action:   "addon.backup_run",
			Pipeline: project,
			App:      addon,
			Resource: "kusoaddon",
			Message:  fmt.Sprintf("manual backup started for addon %s/%s job=%s", project, addon, created.Name),
		})
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"job": created.Name})
}

// manualJobFromCronJob mirrors `kubectl create job --from=cronjob/...`: the
// CronJob's jobTemplate, owned by the CronJob so it is garbage-collected
// with it.
func manualJobFromCronJob(cj *batchv1.CronJob, now time.Time) *batchv1.Job {
	suffix := "-manual-" + strconv.FormatInt(now.Unix(), 10)
	base := cj.Name
	if len(base)+len(suffix) > 63 {
		base = strings.TrimRight(base[:63-len(suffix)], "-.")
	}
	annotations := map[string]string{}
	for k, v := range cj.Spec.JobTemplate.Annotations {
		annotations[k] = v
	}
	annotations["cronjob.kubernetes.io/instantiate"] = "manual"
	labels := map[string]string{}
	for k, v := range cj.Spec.JobTemplate.Labels {
		labels[k] = v
	}
	controller := true
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:        base + suffix,
			Namespace:   cj.Namespace,
			Labels:      labels,
			Annotations: annotations,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "batch/v1",
				Kind:       "CronJob",
				Name:       cj.Name,
				UID:        cj.UID,
				Controller: &controller,
			}},
		},
		Spec: *cj.Spec.JobTemplate.Spec.DeepCopy(),
	}
}

// backupKeyOwned reports whether key is a downloadable backup object under
// prefix: no traversal, not a manifest sidecar, not the bare prefix.
func backupKeyOwned(prefix, key string) bool {
	if prefix == "" || !strings.HasSuffix(prefix, "/") {
		return false
	}
	rest, ok := strings.CutPrefix(key, prefix)
	return ok && rest != "" && validBackupObjectKey(key)
}

func validBackupObjectKey(key string) bool {
	return !strings.Contains(key, "..") && !strings.ContainsRune(key, '\x00') && !isManifestKey(key)
}

// DownloadObject is GET /api/projects/{p}/addons/{a}/backups/object?key=…:
// streams one stored backup object from the backup bucket. Same admin
// secret-read gate as Download — a dump is the whole dataset.
func (h *BackupsHandler) DownloadObject(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	addon := chi.URLParam(r, "addon")
	key := r.URL.Query().Get("key")
	ctx, cancel := context.WithTimeout(r.Context(), dumpTimeout(r.URL.Query().Get("timeout")))
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleViewer) {
		return
	}
	if !callerCanReadSecrets(ctx, h.DB, project) {
		writeErr(w, http.StatusForbidden, "forbidden: downloading a database dump (which contains secret-bearing rows) requires the admin role")
		return
	}
	if key == "" {
		writeErr(w, http.StatusBadRequest, "key required")
		return
	}
	if !validBackupObjectKey(key) {
		writeErr(w, http.StatusBadRequest, "invalid key")
		return
	}
	cr, ns, err := h.ownedAddon(ctx, project, addon)
	if err != nil {
		writeAddonErr(w, err)
		return
	}
	// Ownership is settled before touching S3 so a foreign key is refused
	// even when backups aren't configured.
	owned, bucketOverride := false, ""
	if cr.Spec.UseInstanceAddon != "" {
		loc, err := h.instanceTenantBackups(ctx, ns, cr)
		if err != nil {
			h.Logger.Error("backup: resolve instance tenant backups", "err", err)
			writeErr(w, http.StatusBadGateway, "resolve instance backups: "+err.Error())
			return
		}
		if loc != nil && loc.owns(key) {
			owned, bucketOverride = true, loc.bucket
		}
	} else {
		owned = backupKeyOwned(project+"/"+cr.Name+"/", key)
	}
	if !owned {
		writeErr(w, http.StatusBadRequest, "key does not belong to this addon")
		return
	}
	cli, bucket, err := h.s3Client(ctx)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if cr.Spec.UseInstanceAddon != "" {
		if bucketOverride != "" {
			bucket = bucketOverride
		}
	} else {
		bucket = cr.BackupBucket(bucket)
	}

	out, err := cli.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		var nsk *s3types.NoSuchKey
		var re *awshttp.ResponseError
		if errors.As(err, &nsk) || (errors.As(err, &re) && re.HTTPStatusCode() == http.StatusNotFound) {
			writeErr(w, http.StatusNotFound, "backup object not found")
			return
		}
		h.Logger.Error("backup: get object", "err", err)
		writeErr(w, http.StatusBadGateway, "get object failed: "+err.Error())
		return
	}
	defer out.Body.Close()

	name := strings.NewReplacer(`"`, "", `\`, "", "\r", "", "\n", "").Replace(path.Base(key))
	if h.Audit != nil {
		h.Audit.Log(ctx, audit.Entry{
			User:     auditUser(ctx),
			Severity: "warn",
			Action:   "addon.backup_download",
			Pipeline: project,
			App:      addon,
			Resource: "kusoaddon",
			Message:  fmt.Sprintf("backup object %s downloaded for addon %s/%s", name, project, addon),
		})
	}
	ctype := "application/octet-stream"
	if strings.HasSuffix(key, ".gz") {
		ctype = "application/gzip"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	if out.ContentLength != nil && *out.ContentLength >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(*out.ContentLength, 10))
	}
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, out.Body); err != nil {
		h.Logger.Error("backup: stream object", "key", key, "err", err)
	}
}
