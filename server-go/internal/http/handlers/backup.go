package handlers

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/db"
	"kuso/server/internal/kube"
)

// BackupHandler exposes /api/admin/backup + /api/admin/restore.
//
// Backup: streams `pg_dump` of the metadata DB as gzipped SQL. The
// CLI wraps this and writes the bytes to a file. The kuso-server
// image bundles postgresql-client so pg_dump is on $PATH.
//
// Restore: accepts the same gzipped pg_dump shape and runs a one-
// shot Job that pipes it through psql against the live kuso-postgres.
// We use a Job rather than running psql in-process because:
//
//  1. The kuso-server holds an active *sql.DB pool against the same
//     database — running a destructive restore through that pool
//     would break in-flight requests.
//  2. The Job's pod can pull `postgres:16-alpine` cleanly without
//     bloating the kuso-server image with the full client tools
//     for a once-a-blue-moon op.
//
// On success the handler triggers a rolling restart of kuso-server so
// every replica drops its now-stale connection state.
type BackupHandler struct {
	DB        *db.DB
	Kube      *kube.Client
	Namespace string // kuso-server namespace, default "kuso"
	Logger    *slog.Logger

	// restoreLimitMu + restoreLimitTokens implement a simple per-
	// process rate limit on /api/admin/restore so a leaked admin
	// token can't spawn unbounded Jobs + Secrets. Bucket of 5,
	// refilled at 1/hour. Cluster-wide cap (not per-admin) is the
	// right granularity — a real restore is a once-per-incident op.
	restoreLimitMu     sync.Mutex
	restoreLimitTokens float64
	restoreLimitLast   time.Time
}

// NewBackupHandler returns a configured handler. Kube + Namespace
// are needed for restore; backup works without them (in-process
// pg_dump). Pass nil Kube to disable the restore path.
func NewBackupHandler(database *db.DB, kc *kube.Client, namespace string, logger *slog.Logger) *BackupHandler {
	if namespace == "" {
		namespace = "kuso"
	}
	return &BackupHandler{DB: database, Kube: kc, Namespace: namespace, Logger: logger}
}

// takeRestoreToken returns true if the cluster-wide restore bucket
// has capacity. Burst of 5 (covers an admin retrying a flaky
// restore), refill 1/hour (real restores are rare incident ops).
// Returns false on quota exhaustion; caller responds 429.
func (h *BackupHandler) takeRestoreToken() bool {
	const cap = 5.0
	const refillPerSec = 1.0 / 3600.0
	h.restoreLimitMu.Lock()
	defer h.restoreLimitMu.Unlock()
	now := time.Now()
	if h.restoreLimitLast.IsZero() {
		h.restoreLimitTokens = cap
	} else {
		h.restoreLimitTokens += now.Sub(h.restoreLimitLast).Seconds() * refillPerSec
		if h.restoreLimitTokens > cap {
			h.restoreLimitTokens = cap
		}
	}
	h.restoreLimitLast = now
	if h.restoreLimitTokens < 1 {
		return false
	}
	h.restoreLimitTokens--
	return true
}

// Mount registers admin-only routes.
func (h *BackupHandler) Mount(r chi.Router) {
	if h == nil {
		return
	}
	r.Get("/api/admin/backup", h.Download)
	r.Post("/api/admin/restore", h.Upload)
	r.Get("/api/admin/restore/{jobName}", h.RestoreStatus)
}

// Download streams `pg_dump --format=plain --no-owner --no-acl --clean
// --if-exists` as gzip. We invoke pg_dump in-process; the kuso-server
// image must bundle postgresql-client (Dockerfile dependency).
//
// Streaming so we don't buffer 50 GB of dump in memory on a big
// install. If pg_dump fails mid-stream, the gzip will be truncated
// and the client will error on decompress — preferable to silently
// shipping a half-dump.
func (h *BackupHandler) Download(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	dsn := h.dsnForPgTools()
	if dsn == "" {
		http.Error(w, "backup unavailable: no DSN resolved (kuso-postgres-conn Secret missing?)", http.StatusServiceUnavailable)
		return
	}
	timeout := 5 * time.Minute
	if v := r.URL.Query().Get("timeout"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 && d < time.Hour {
			timeout = d
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	stamp := time.Now().UTC().Format("20060102T150405Z")
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="kuso-backup-%s.sql.gz"`, stamp))

	cmd := exec.CommandContext(ctx, "pg_dump",
		"--format=plain", "--no-owner", "--no-acl", "--clean", "--if-exists",
		dsn,
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, "pg_dump pipe: "+err.Error(), http.StatusInternalServerError)
		return
	}
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		http.Error(w, "pg_dump start: "+err.Error(), http.StatusInternalServerError)
		return
	}
	gz := gzip.NewWriter(w)
	if _, err := io.Copy(gz, stdout); err != nil {
		h.Logger.Error("backup: copy", "err", err)
		_ = cmd.Process.Kill()
		_ = gz.Close()
		return
	}
	if err := cmd.Wait(); err != nil {
		// Slurp stderr for the log; the body is mid-stream so we
		// can't change response code now.
		var buf strings.Builder
		if stderr != nil {
			_, _ = io.Copy(&buf, stderr)
		}
		h.Logger.Error("backup: pg_dump wait", "err", err, "stderr", buf.String())
	}
	_ = gz.Close()
}

// Upload accepts a gzipped pg_dump and runs a one-shot Job to apply
// it. Returns 202 with the Job name; CLI polls
// /api/admin/restore/{jobName} for status. On success the kuso-server
// rollout fires automatically.
func (h *BackupHandler) Upload(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if h.Kube == nil {
		http.Error(w, "restore unavailable: no kube client", http.StatusServiceUnavailable)
		return
	}
	// Cluster-wide rate limit: 5 burst, 1/hour refill. Without this,
	// a leaked admin token can spawn unbounded restore Jobs + 900 KiB
	// dump Secrets that leak etcd until either they hit the Job's
	// TTL (7 days for failed) or an admin manually cleans up.
	if !h.takeRestoreToken() {
		http.Error(w, "restore rate limit exceeded — wait a few minutes between restores",
			http.StatusTooManyRequests)
		return
	}
	// 10 GB hard cap on the upload — nobody's metadata DB is that
	// big and the cap stops a runaway client.
	body, err := io.ReadAll(io.LimitReader(r.Body, 10<<30))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(body) < 32 {
		http.Error(w, "empty or truncated dump", http.StatusBadRequest)
		return
	}
	// Secret data caps out near 1 MiB on most kubelets. For large
	// dumps an operator should pg_restore directly against
	// kuso-postgres; we surface a clear error rather than fail in
	// confusing ways.
	if len(body) > 900*1024 {
		http.Error(w,
			"dump exceeds 900 KiB — for larger dumps run pg_restore directly against kuso-postgres (see docs/BACKUP_RESTORE.md)",
			http.StatusRequestEntityTooLarge)
		return
	}

	stamp := time.Now().UTC().Format("20060102t150405z")
	jobName := "kuso-restore-" + stamp
	secretName := "kuso-restore-data-" + stamp

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	if err := h.createRestoreSecret(ctx, secretName, body); err != nil {
		h.Logger.Error("restore: create secret", "err", err)
		http.Error(w, "create secret: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.createRestoreJob(ctx, jobName, secretName); err != nil {
		// Best-effort cleanup on Job-create failure so we don't
		// leak the dump Secret (up to 900 KiB of sensitive data).
		// Detach from r.Context() since the client has already
		// disconnected on the error response path — but cap at 5s
		// so a slow apiserver doesn't wedge the handler.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if cerr := h.deleteRestoreSecret(cleanupCtx, secretName); cerr != nil {
			h.Logger.Warn("restore: cleanup data secret after job-create failure", "secret", secretName, "err", cerr)
		}
		h.Logger.Error("restore: create job", "err", err)
		http.Error(w, "create job: "+err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]string{
		"jobName":    jobName,
		"secretName": secretName,
		"statusUrl":  "/api/admin/restore/" + jobName,
		"hint":       "poll statusUrl until phase=Succeeded; the kuso-server rollout fires automatically on first success",
	})
}

// RestoreStatus reports Job phase and triggers the rollout the first
// time the Job flips to Succeeded. Idempotent via a Job annotation.
func (h *BackupHandler) RestoreStatus(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if h.Kube == nil {
		http.Error(w, "restore unavailable: no kube client", http.StatusServiceUnavailable)
		return
	}
	jobName := chi.URLParam(r, "jobName")
	if !strings.HasPrefix(jobName, "kuso-restore-") {
		http.Error(w, "bad job name", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	job, err := h.Kube.Clientset.BatchV1().Jobs(h.Namespace).Get(ctx, jobName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			http.Error(w, "job not found", http.StatusNotFound)
			return
		}
		http.Error(w, "get job: "+err.Error(), http.StatusInternalServerError)
		return
	}
	phase := "Running"
	if job.Status.Succeeded > 0 {
		phase = "Succeeded"
	} else if job.Status.Failed > 0 {
		phase = "Failed"
	}
	resp := map[string]any{
		"jobName": jobName,
		"phase":   phase,
		"active":  job.Status.Active,
	}
	// Both terminal phases need the dump Secret cleaned up — leaving
	// it around on Failed lets a sequence of failed restores
	// accumulate 900 KiB Secrets that survive the Job's 7-day TTL.
	// Idempotent: deleteRestoreSecret on a missing Secret is fine.
	cleanupSecret := func() {
		secretName := "kuso-restore-data-" + strings.TrimPrefix(jobName, "kuso-restore-")
		// Use a fresh bounded context so this isn't tied to r.Context()
		// (which was already used for status read) — but cap at 5s so
		// a stuck apiserver doesn't block the status response.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := h.deleteRestoreSecret(cleanupCtx, secretName); err != nil {
			h.Logger.Warn("restore: cleanup data secret", "secret", secretName, "err", err)
		}
	}
	switch phase {
	case "Succeeded":
		if job.Annotations == nil || job.Annotations["kuso.sislelabs.com/rollout-triggered"] != "true" {
			if err := h.triggerKusoServerRollout(ctx); err != nil {
				h.Logger.Error("restore: rollout", "err", err)
				resp["rolloutError"] = err.Error()
			} else {
				h.markRolloutTriggered(ctx, jobName)
				resp["rolloutTriggered"] = true
			}
		}
		cleanupSecret()
	case "Failed":
		cleanupSecret()
	}
	writeJSON(w, http.StatusOK, resp)
}

// dsnForPgTools resolves the libpq DSN from the kuso-postgres-conn
// Secret. We don't re-use KUSO_DB_DSN directly because that env var
// might be set to a host-mapped form during dev; the in-cluster
// Service hostname is what pg_dump should target.
func (h *BackupHandler) dsnForPgTools() string {
	if h.Kube == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sec, err := h.Kube.Clientset.CoreV1().Secrets(h.Namespace).Get(ctx, "kuso-postgres-conn", metav1.GetOptions{})
	if err != nil {
		return ""
	}
	if v, ok := sec.Data["dsn"]; ok && len(v) > 0 {
		return string(v)
	}
	// CNPG-bootstrap shape uses 'username'; legacy / external-Postgres
	// installs may have 'user'. Try both.
	user := string(sec.Data["username"])
	if user == "" {
		user = string(sec.Data["user"])
	}
	pass := string(sec.Data["password"])
	dbName := string(sec.Data["database"])
	if dbName == "" {
		dbName = string(sec.Data["dbname"])
	}
	host := string(sec.Data["host"])
	if host == "" {
		// CNPG primary is at <cluster>-rw; that's the right
		// fallback now that the bundled Postgres is CNPG-managed.
		// Pre-v0.9.38 the StatefulSet was just kuso-postgres, but
		// upgraded clusters get the dsn key populated by the
		// dsn-stamp Job and never hit this branch.
		host = "kuso-postgres-rw"
	}
	port := string(sec.Data["port"])
	if port == "" {
		port = "5432"
	}
	if user == "" || dbName == "" {
		return ""
	}
	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable",
		url.QueryEscape(user), url.QueryEscape(pass), host, port, url.QueryEscape(dbName))
}

func (h *BackupHandler) createRestoreSecret(ctx context.Context, name string, dump []byte) error {
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: h.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":      "kuso-restore",
				"app.kubernetes.io/component": "data",
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{"dump.sql.gz": dump},
	}
	_, err := h.Kube.Clientset.CoreV1().Secrets(h.Namespace).Create(ctx, sec, metav1.CreateOptions{})
	return err
}

func (h *BackupHandler) deleteRestoreSecret(ctx context.Context, name string) error {
	return h.Kube.Clientset.CoreV1().Secrets(h.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
}

func (h *BackupHandler) createRestoreJob(ctx context.Context, jobName, secretName string) error {
	zero := int32(0)
	one := int32(1)
	ttl := int32(7 * 24 * 3600) // 7d so failed jobs stick around for triage
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: h.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":      "kuso-restore",
				"app.kubernetes.io/component": "job",
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &zero,
			Completions:             &one,
			Parallelism:             &one,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app.kubernetes.io/name":      "kuso-restore",
						"app.kubernetes.io/component": "psql",
					},
				},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					// Pod-level security context: drop root, no
					// privilege escalation, seccomp default. The
					// pod runs psql against a Secret-backed dump —
					// nothing privileged needed. postgres:16-alpine
					// uses uid 70 (the postgres user) by default
					// when run with non-root via runAsUser; we let
					// kube pick an arbitrary high uid via
					// runAsNonRoot=true so the image's own uid 70
					// isn't a hard requirement.
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: ptrBool(true),
						RunAsUser:    ptrInt64(70),
						RunAsGroup:   ptrInt64(70),
						FSGroup:      ptrInt64(70),
						SeccompProfile: &corev1.SeccompProfile{
							Type: corev1.SeccompProfileTypeRuntimeDefault,
						},
					},
					Containers: []corev1.Container{{
						Name:            "psql",
						Image:           "postgres:16-alpine",
						ImagePullPolicy: corev1.PullIfNotPresent,
						Command:         []string{"/bin/sh", "-ec"},
						Args: []string{`
set -e
echo "[restore] starting at $(date -u)"
gunzip -c /data/dump.sql.gz | psql "$DSN" -v ON_ERROR_STOP=1
echo "[restore] done at $(date -u)"
`},
						Env: []corev1.EnvVar{{
							Name: "DSN",
							ValueFrom: &corev1.EnvVarSource{
								SecretKeyRef: &corev1.SecretKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{Name: "kuso-postgres-conn"},
									Key:                  "dsn",
								},
							},
						}},
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: ptrBool(false),
							ReadOnlyRootFilesystem:   ptrBool(true),
							Capabilities: &corev1.Capabilities{
								Drop: []corev1.Capability{"ALL"},
							},
						},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "dump", MountPath: "/data", ReadOnly: true},
							// /tmp is writable so psql can stage
							// its temp files; rootfs stays read-only.
							{Name: "tmp", MountPath: "/tmp"},
						},
					}},
					Volumes: []corev1.Volume{
						{
							Name: "dump",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName:  secretName,
									DefaultMode: ptrInt32(0o400),
								},
							},
						},
						{
							Name: "tmp",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
					},
				},
			},
		},
	}
	_, err := h.Kube.Clientset.BatchV1().Jobs(h.Namespace).Create(ctx, job, metav1.CreateOptions{})
	return err
}

// triggerKusoServerRollout is the in-cluster equivalent of
// `kubectl rollout restart deploy/kuso-server`. Kube watches the pod-
// template annotation and rolls every replica.
func (h *BackupHandler) triggerKusoServerRollout(ctx context.Context) error {
	patch := fmt.Sprintf(
		`{"spec":{"template":{"metadata":{"annotations":{"kuso.sislelabs.com/restartedAt":"%s"}}}}}`,
		time.Now().UTC().Format(time.RFC3339),
	)
	_, err := h.Kube.Clientset.AppsV1().Deployments(h.Namespace).Patch(
		ctx, "kuso-server", "application/strategic-merge-patch+json",
		[]byte(patch), metav1.PatchOptions{},
	)
	return err
}

func (h *BackupHandler) markRolloutTriggered(ctx context.Context, jobName string) {
	patch := []byte(`{"metadata":{"annotations":{"kuso.sislelabs.com/rollout-triggered":"true"}}}`)
	_, err := h.Kube.Clientset.BatchV1().Jobs(h.Namespace).Patch(
		ctx, jobName, "application/strategic-merge-patch+json",
		patch, metav1.PatchOptions{},
	)
	if err != nil && h.Logger != nil {
		h.Logger.Warn("restore: mark rollout-triggered", "err", err)
	}
}

// Tiny pointer helpers — kube types want *bool, *int32, *int64 in
// SecurityContext/SeccompProfile/etc. Inline-allocating addresses of
// literals isn't allowed; these stay alongside the only caller.
func ptrBool(b bool) *bool       { return &b }
func ptrInt32(i int32) *int32    { return &i }
func ptrInt64(i int64) *int64    { return &i }
