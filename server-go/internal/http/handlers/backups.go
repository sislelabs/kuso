package handlers

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/go-chi/chi/v5"
	_ "github.com/lib/pq" // postgres driver registers itself with database/sql
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/addons"
	"kuso/server/internal/audit"
	"kuso/server/internal/auth"
	"kuso/server/internal/backup"
	"kuso/server/internal/db"
	"kuso/server/internal/httpx"
	"kuso/server/internal/kube"
)

// BackupsHandler exposes /api/admin/backup-settings (CRUD on the
// kuso-backup-s3 Secret) and /api/projects/{p}/addons/{a}/backups
// (list + restore, gated on the same Secret existing).
type BackupsHandler struct {
	Kube  *kube.Client
	DB    *db.DB
	Audit *audit.Service
	// Addons resolves (project, addon) URL pairs to ownership-checked
	// CRs and to the project's execution namespace. Every addon-scoped
	// path here goes through it (see ownedAddon) — the raw CRName()
	// string mapping tolerates pre-qualified names, so with overlapping
	// project names ("foo" vs "foo-bar") it could resolve a sibling
	// project's addon, and the static home namespace is wrong for
	// projects with a per-project execution namespace.
	Addons    *addons.Service
	Namespace string
	Logger    *slog.Logger
}

// ownedAddon fetches the addon CR for (project, addon), verifying it
// belongs to project, and returns it with the project's execution
// namespace (where its -conn Secret and pods live). Falls back to a
// throwaway addons.Service pinned to the home namespace when Addons
// isn't wired (tests) — ownership is still enforced.
func (h *BackupsHandler) ownedAddon(ctx context.Context, project, addon string) (*kube.KusoAddon, string, error) {
	svc := h.Addons
	if svc == nil {
		svc = addons.New(h.Kube, h.Namespace)
	}
	cr, err := svc.GetOwned(ctx, project, addon)
	if err != nil {
		return nil, "", err
	}
	return cr, svc.NamespaceFor(ctx, project), nil
}

// writeAddonErr maps an addon-resolution failure to HTTP: an unknown
// (or other-project-owned) addon is a 404, anything else a 502.
func writeAddonErr(w http.ResponseWriter, err error) {
	if errors.Is(err, addons.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeErr(w, http.StatusBadGateway, err.Error())
}

const backupSecretName = "kuso-backup-s3"

func (h *BackupsHandler) Mount(r chi.Router) {
	r.Get("/api/admin/backup-settings", h.GetSettings)
	r.Put("/api/admin/backup-settings", h.PutSettings)
	r.Get("/api/projects/{project}/addons/{addon}/backups", h.List)
	r.Post("/api/projects/{project}/addons/{addon}/backups/restore", h.Restore)
	// Direct on-demand dump, streamed to the caller. Independent of the
	// kuso-backup-s3 config, so it works when List/Restore return 503.
	r.Get("/api/projects/{project}/addons/{addon}/backups/download", h.Download)
	// SQL browser: list tables + run a read-only SELECT against the
	// addon's postgres. The query is wrapped in a read-only
	// transaction with a statement_timeout — defence in depth against
	// a stray `; DROP TABLE` or a runaway scan.
	r.Get("/api/projects/{project}/addons/{addon}/sql/databases", h.SQLDatabases)
	r.Get("/api/projects/{project}/addons/{addon}/sql/tables", h.SQLTables)
	r.Post("/api/projects/{project}/addons/{addon}/sql/query", h.SQLQuery)
	// Structured data browser/editor (sql_data.go): per-table schema +
	// paginated rows + PK-targeted insert/update/delete. The raw /sql/query
	// runner above stays read-only; ALL writes flow through these.
	r.Get("/api/projects/{project}/addons/{addon}/sql/columns", h.SQLColumns)
	r.Get("/api/projects/{project}/addons/{addon}/sql/rows", h.SQLRows)
	r.Post("/api/projects/{project}/addons/{addon}/sql/rows", h.SQLInsertRow)
	r.Patch("/api/projects/{project}/addons/{addon}/sql/rows", h.SQLUpdateRow)
	r.Delete("/api/projects/{project}/addons/{addon}/sql/rows", h.SQLDeleteRow)
}

// BackupSettings is the wire shape. We never echo the secret access
// key on GET — clients see whether it's set, not the value itself.
type BackupSettings struct {
	Bucket          string `json:"bucket"`
	Endpoint        string `json:"endpoint"`
	Region          string `json:"region"`
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey,omitempty"`
	HasSecret       bool   `json:"hasSecret"`
}

func (h *BackupsHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	ctx, cancel := backupCtx(r)
	defer cancel()
	sec, err := h.Kube.Clientset.CoreV1().Secrets(h.Namespace).Get(ctx, backupSecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		writeJSON(w, http.StatusOK, BackupSettings{})
		return
	}
	if err != nil {
		h.Logger.Error("backup: get secret", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	out := BackupSettings{
		Bucket:      string(sec.Data["bucket"]),
		Endpoint:    string(sec.Data["endpoint"]),
		Region:      string(sec.Data["region"]),
		AccessKeyID: string(sec.Data["accessKeyId"]),
		HasSecret:   len(sec.Data["secretAccessKey"]) > 0,
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *BackupsHandler) PutSettings(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var req BackupSettings
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if req.Bucket == "" || req.Endpoint == "" || req.AccessKeyID == "" {
		writeErr(w, http.StatusBadRequest, "bucket, endpoint, accessKeyId required")
		return
	}
	// Endpoint goes verbatim into the backup Job's S3 client. Same
	// SSRF surface as the notification webhook URL — IMDS, RFC1918,
	// .svc DNS would all succeed against an admin-set endpoint.
	if err := validateWebhookURL(req.Endpoint); err != nil {
		writeErr(w, http.StatusBadRequest, "endpoint: "+err.Error())
		return
	}
	ctx, cancel := backupCtx(r)
	defer cancel()

	// Read the existing Secret so we can preserve secretAccessKey
	// when the client didn't send a new one (PUT-with-unchanged
	// semantic — empty field means "don't touch").
	var existing *corev1.Secret
	if got, err := h.Kube.Clientset.CoreV1().Secrets(h.Namespace).Get(ctx, backupSecretName, metav1.GetOptions{}); err == nil {
		existing = got
	}
	data := map[string][]byte{
		"bucket":      []byte(req.Bucket),
		"endpoint":    []byte(req.Endpoint),
		"region":      []byte(req.Region),
		"accessKeyId": []byte(req.AccessKeyID),
	}
	if req.SecretAccessKey != "" {
		data["secretAccessKey"] = []byte(req.SecretAccessKey)
	} else if existing != nil {
		data["secretAccessKey"] = existing.Data["secretAccessKey"]
	}
	if len(data["secretAccessKey"]) == 0 {
		writeErr(w, http.StatusBadRequest, "secretAccessKey required on first save")
		return
	}

	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      backupSecretName,
			Namespace: h.Namespace,
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "kuso-server"},
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}
	if existing == nil {
		_, err := h.Kube.Clientset.CoreV1().Secrets(h.Namespace).Create(ctx, sec, metav1.CreateOptions{})
		if err != nil {
			h.Logger.Error("backup: create secret", "err", err)
			writeErr(w, http.StatusInternalServerError, "internal")
			return
		}
	} else {
		sec.ResourceVersion = existing.ResourceVersion
		_, err := h.Kube.Clientset.CoreV1().Secrets(h.Namespace).Update(ctx, sec, metav1.UpdateOptions{})
		if err != nil {
			h.Logger.Error("backup: update secret", "err", err)
			writeErr(w, http.StatusInternalServerError, "internal")
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// BackupObject is one item in the list — the S3 key, size, and the
// timestamp parsed out of the key suffix (we name dumps with a
// strict format so this is reliable).
type BackupObject struct {
	Key  string    `json:"key"`
	Size int64     `json:"size"`
	When time.Time `json:"when"`
}

// isManifestKey reports whether an S3 key is a backup manifest sidecar
// rather than a restorable artifact.
func isManifestKey(key string) bool {
	return strings.HasSuffix(key, ".manifest.json")
}

func (h *BackupsHandler) List(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	addon := chi.URLParam(r, "addon")
	ctx, cancel := backupCtx(r)
	defer cancel()
	// Listing an addon's backups returns only S3 object metadata (keys,
	// sizes, timestamps) — not the data itself — so editor is enough.
	// The data-revealing surfaces (SQL browser, secret values) are
	// separately admin-gated in role-system v2.
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleViewer) {
		return
	}

	// Ownership: resolve through ownedAddon (GetOwned) rather than a raw
	// CRName() — CRName tolerates pre-qualified input, so a same-project
	// short/FQN alias could otherwise list a sibling addon's backup keys.
	// The FQN below is derived from the verified CR's name, never the raw
	// caller input. Data-revealing paths (Download/Restore/SQL) already do
	// this; List must too so the file's invariant holds on every path.
	cr, _, err := h.ownedAddon(ctx, project, addon)
	if err != nil {
		writeAddonErr(w, err)
		return
	}

	cli, bucket, err := h.s3Client(ctx)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	// CronJob uploads to s3://bucket/<project>/<addon-fqn>/<ts>.sql.gz
	// where <addon-fqn> = the helm release name = the CR's metadata.name.
	addonFQN := cr.Name
	prefix := project + "/" + addonFQN + "/"
	out, err := cli.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
		Prefix: aws.String(prefix),
	})
	if err != nil {
		h.Logger.Error("backup: list", "err", err)
		writeErr(w, http.StatusBadGateway, "list failed: "+err.Error())
		return
	}
	items := make([]BackupObject, 0, len(out.Contents))
	for _, o := range out.Contents {
		key := aws.ToString(o.Key)
		if isManifestKey(key) {
			continue // sidecar, not a restorable backup
		}
		size := aws.ToInt64(o.Size)
		when := time.Time{}
		if o.LastModified != nil {
			when = *o.LastModified
		}
		items = append(items, BackupObject{Key: key, Size: size, When: when})
	}
	writeJSON(w, http.StatusOK, items)
}

// RestoreRequest names which backup to restore. We accept the full
// S3 key so the client doesn't have to reconstruct the prefix.
type RestoreRequest struct {
	Key string `json:"key"`
	// Into is the destination addon. Empty = restore in-place
	// (overwrites the source addon's data — destructive). Non-empty
	// = restore into the addon with this name; the addon must
	// already exist (create it via the normal addon flow first
	// with the same kind).
	Into string `json:"into,omitempty"`
	// Confirm must echo the destination addon name for an IN-PLACE
	// restore — i.e. whenever the destination CR resolves to the SAME CR
	// as the source (Into == "", Into == source, or Into a different
	// alias of the same addon such as the short vs fully-qualified name).
	// The restore streams the dump into the target's own database via
	// `psql --single-transaction`, overwriting live data — the same blast
	// radius as an addon Delete, which is why it demands the same typed
	// acknowledgement. Ignored only when restoring into a GENUINELY
	// distinct sibling addon (non-destructive to the source).
	Confirm string `json:"confirm,omitempty"`
}

// Restore creates a one-shot Job that downloads the backup and pipes
// it into psql. The Job inherits the cluster-wide kuso-backup-s3
// Secret + the per-addon -conn Secret for DB credentials. Returns
// the Job name so the client can tail logs.
func (h *BackupsHandler) Restore(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	addon := chi.URLParam(r, "addon")
	var req RestoreRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Key == "" {
		writeErr(w, http.StatusBadRequest, "key required")
		return
	}
	ctx, cancel := backupCtx(r)
	defer cancel()
	// Editor — Restore overwrites the live DB with a snapshot
	// (destructive write). It does not return data to the caller, so it's
	// a write-grade action, not a secret-read one; editor (the v2 write
	// role) is the right bar. The data-READING surfaces (SQL browser,
	// secret values, export) are separately admin-gated.
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleEditor) {
		return
	}
	// Cross-tenant key guard: req.Key arrives from the client and is
	// splatted into the restore Job's KEY env var. List scopes by
	// prefix (project + "/" + addonFQN + "/"); Restore must enforce
	// the same — restoring across addons within the same project is
	// fine, across projects is not. The explicit empty-project check
	// keeps the gate sound if a future refactor makes the URL param
	// optional (HasPrefix("foo", "/") is true for any leading slash).
	if project == "" || !strings.HasPrefix(req.Key, project+"/") {
		writeErr(w, http.StatusBadRequest, "key must live under this project's prefix")
		return
	}
	// `..` traversal escapes the project prefix. The S3 SDK doesn't
	// decode percent-encoding and neither does `aws s3 cp` in the
	// Job, so a literal `..` is the only shape we need to reject.
	if strings.Contains(req.Key, "..") || strings.ContainsRune(req.Key, '\x00') {
		writeErr(w, http.StatusBadRequest, "invalid key")
		return
	}

	// Destination addon — defaults to the source (in-place /
	// destructive). When `into` is set, we restore into a sibling
	// addon, leaving the source untouched. The new addon must
	// already exist; we don't create it here so the flow stays
	// auditable (you saw the addon before you ran restore).
	destAddon := addon
	if req.Into != "" {
		destAddon = req.Into
	}
	// Resolve BOTH addons through the ownership-checked path. This (a)
	// yields the project's execution namespace — the -conn Secret the
	// restore Job references and the Job itself must live there, not in
	// the static home namespace — and (b) blocks a cross-project write:
	// `into` accepts pre-qualified names, so without the ownership check
	// a foo-authorized caller passing into="foo-bar-pg" would restore
	// INTO the sibling project foo-bar's database.
	srcCR, ns, err := h.ownedAddon(ctx, project, addon)
	if err != nil {
		writeAddonErr(w, err)
		return
	}
	destCR := srcCR
	if req.Into != "" {
		if destCR, _, err = h.ownedAddon(ctx, project, destAddon); err != nil {
			writeAddonErr(w, err)
			return
		}
		// Kind compatibility — refuse to point a postgres dump at a
		// redis addon (the seed Job would only fail mid-restore with
		// an opaque psql error after the dump's already partially
		// streamed).
		if srcCR.Spec.Kind != "" && destCR.Spec.Kind != "" &&
			srcCR.Spec.Kind != destCR.Spec.Kind {
			writeErr(w,

				http.StatusBadRequest, fmt.Sprintf("kind mismatch: source addon %q is %s, destination %q is %s",
					addon, srcCR.Spec.Kind, destAddon, destCR.Spec.Kind))

			return
		}
	}
	// Typed-confirmation guard for the DESTRUCTIVE in-place restore.
	// Compare the RESOLVED CR names, not the raw request strings: `pg` and
	// `myproj-pg` both resolve to the same CR, so a caller could pass
	// into="myproj-pg" while the URL addon is "pg" and — under the old
	// raw-string check (destAddon == addon) — dodge the confirm gate while
	// still overwriting the source addon's live data in place. When the
	// destination CR IS the source CR, the restore Job applies the dump onto
	// the target's own database (all-or-nothing psql --single-transaction),
	// overwriting live data with no undo — the same blast radius as an addon
	// Delete. Demand the caller echo the destination addon name in `confirm`,
	// mirroring AddonsHandler.Delete's ?confirm=<addon> gate. Restoring into a
	// GENUINELY DISTINCT sibling addon leaves the source untouched, so it's
	// exempt.
	if inPlaceRestoreNeedsConfirm(srcCR.Name, destCR.Name, destAddon, req.Confirm) {
		writeErr(w,

			http.StatusBadRequest, "in-place restore overwrites live data — set \"confirm\":\"<addon-name>\" to acknowledge (or restore into a different addon via \"into\")")

		return
	}
	// The restore Job's env sources BUCKET/S3_ENDPOINT/AWS creds from the
	// kuso-backup-s3 Secret, which SetSettings only ever writes into the
	// HOME namespace. For a project with a per-project execution namespace,
	// a Job in `ns` referencing that secret would fail
	// CreateContainerConfigError ("secret kuso-backup-s3 not found") and,
	// with BackoffLimit=0, never run — while the API still returned 200 +
	// jobName. Mirror the secret into `ns` first (or fail with a clear
	// "backups not configured" error if it doesn't exist in home) so the
	// Job can actually start. The scheduled-backup CronJob path is
	// chart-rendered into the project ns with its own secret, so it's
	// unaffected by this gap.
	if ns != h.Namespace {
		if err := h.mirrorBackupSecret(ctx, ns); err != nil {
			if errors.Is(err, addons.ErrNotFound) {
				writeErr(w, http.StatusPreconditionFailed, "backups not configured: kuso-backup-s3 secret missing (set backup settings first)")
				return
			}
			h.Logger.Error("backup: mirror s3 secret into project ns", "err", err, "ns", ns)
			writeErr(w, http.StatusInternalServerError, "internal")
			return
		}
	}
	releaseName := destCR.Name
	jobName := fmt.Sprintf("%s-restore-%d", releaseName, time.Now().Unix())

	// Resolve the restore shell by the destination addon's kind. An
	// unbackable kind is a 400 rather than a doomed Job. (src/dest kinds
	// are already validated equal above when restoring into a sibling.)
	restoreShell, err := restoreScriptForKind(destCR.Spec.Kind)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	restoreEnv := append(restoreConnEnv(destCR.Spec.Kind, releaseName), restoreS3Env()...)

	one := int32(1)
	zero := int32(0)
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			// The project's execution namespace: the <release>-conn
			// Secret the env refs below resolve against is namespace-
			// local, so a Job in the home namespace can't start for
			// projects with a per-project namespace. Matches where the
			// chart's scheduled backup CronJob runs.
			Name:      jobName,
			Namespace: ns,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by":    "kuso-server",
				"kuso.sislelabs.com/role":         "restore",
				"kuso.sislelabs.com/project":      project,
				"kuso.sislelabs.com/addon":        destAddon,
				"kuso.sislelabs.com/source-addon": addon,
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
						Name:            "restore",
						Image:           "ghcr.io/sislelabs/kuso-backup:latest",
						ImagePullPolicy: corev1.PullIfNotPresent,
						Command:         []string{"sh", "-c"},
						Args:            []string{restoreShell},
						// KEY names the artifact; the rest of the env is the
						// kind-aware connection params (restoreConnEnv) +
						// shared S3 creds (restoreS3Env), computed above.
						Env: append([]corev1.EnvVar{{Name: "KEY", Value: req.Key}}, restoreEnv...),
					}},
				},
			},
		},
	}
	created, err := h.Kube.Clientset.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		h.Logger.Error("backup: create restore job", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	// Audit: destructive cross-DB write. Logged regardless of
	// outcome so a half-completed restore (Job created but pod
	// crashed) still leaves a trail.
	if h.Audit != nil {
		uid := ""
		if c, ok := auth.ClaimsFromContext(ctx); ok && c != nil {
			uid = c.UserID
		}
		h.Audit.Log(ctx, audit.Entry{
			User:     uid,
			Severity: "warn",
			Action:   "addon.restore",
			Pipeline: project,
			App:      destAddon,
			Resource: "kusoaddon",
			Message:  fmt.Sprintf("restore from key=%s into project=%s addon=%s job=%s", req.Key, project, destAddon, created.Name),
		})
	}
	writeJSON(w, http.StatusCreated, map[string]string{"job": created.Name})
}

// inPlaceRestoreNeedsConfirm decides whether the destructive-in-place
// confirmation gate applies. It compares the RESOLVED CR names (srcName /
// destName) rather than the raw request strings, so short-name/FQN aliasing
// (`pg` vs `myproj-pg`, both the same CR) can't be used to slip an in-place
// overwrite past the gate. When the destination CR is the source CR the
// restore overwrites live data in place and the caller must have echoed the
// destination addon name in confirm; a genuinely distinct sibling destination
// is exempt.
func inPlaceRestoreNeedsConfirm(srcName, destName, destAddon, confirm string) bool {
	if destName != srcName {
		return false // distinct destination CR — non-destructive to source
	}
	return confirm != destAddon
}

// restoreScriptForKind returns the restore shell for an addon kind via
// the producer registry. Unknown/unbackable kinds are rejected so the
// caller can return a clear 400 instead of minting a doomed Job. The
// per-kind script downloads the artifact + its sibling manifest, verifies
// the sha256 before applying (aborting on mismatch), and warns-but-
// proceeds when a pre-manifest backup has no manifest.
func restoreScriptForKind(kind string) (string, error) {
	p, ok := backup.NewDefaultRegistry().For(kind)
	if !ok {
		return "", fmt.Errorf("addon kind %q is not restorable", kind)
	}
	return p.RestoreScript(), nil
}

// restoreConnEnv returns the addon connection env the restore Job needs
// for a given kind, sourced from the addon's <release>-conn Secret.
// postgres keeps the exact POSTGRES_* set it always had; mongodb needs
// only the MONGO_URL connection URI.
func restoreConnEnv(kind, releaseName string) []corev1.EnvVar {
	if kind == "mongodb" {
		return []corev1.EnvVar{
			envFromSecret("MONGO_URL", releaseName+"-conn", "MONGO_URL"),
		}
	}
	if kind == "mysql" {
		return []corev1.EnvVar{
			envFromSecret("MYSQL_HOST", releaseName+"-conn", "MYSQL_HOST"),
			envFromSecret("MYSQL_USER", releaseName+"-conn", "MYSQL_USER"),
			envFromSecret("MYSQL_DB", releaseName+"-conn", "MYSQL_DB"),
			envFromSecret("MYSQL_PASSWORD", releaseName+"-conn", "MYSQL_PASSWORD"),
		}
	}
	// postgres (default) — source ALL connection parameters from the
	// addon's <release>-conn Secret so the restore Job tracks whatever the
	// chart wrote (same source of truth as the application pods).
	return []corev1.EnvVar{
		envFromSecret("POSTGRES_HOST", releaseName+"-conn", "POSTGRES_HOST"),
		envFromSecret("POSTGRES_USER", releaseName+"-conn", "POSTGRES_USER"),
		envFromSecret("POSTGRES_DB", releaseName+"-conn", "POSTGRES_DB"),
		envFromSecret("POSTGRES_PASSWORD", releaseName+"-conn", "POSTGRES_PASSWORD"),
	}
}

// restoreS3Env returns the S3 credential env shared by all restore kinds.
func restoreS3Env() []corev1.EnvVar {
	return []corev1.EnvVar{
		envFromSecret("BUCKET", backupSecretName, "bucket"),
		envFromSecret("S3_ENDPOINT", backupSecretName, "endpoint"),
		envFromSecret("AWS_ACCESS_KEY_ID", backupSecretName, "accessKeyId"),
		envFromSecret("AWS_SECRET_ACCESS_KEY", backupSecretName, "secretAccessKey"),
		envFromSecretOptional("AWS_DEFAULT_REGION", backupSecretName, "region"),
	}
}

// mirrorBackupSecret copies the home-namespace kuso-backup-s3 Secret
// into a target (project execution) namespace so a restore Job running
// there can resolve its BUCKET/S3_ENDPOINT/AWS-credential env refs. The
// secret is only ever written into the home namespace by SetSettings, so
// without this a restore Job in a per-project namespace fails
// CreateContainerConfigError and (BackoffLimit=0) silently never runs.
//
// Idempotent: updates the mirror's data in place when it already exists.
// Fresh ObjectMeta is built (no resourceVersion/uid/namespace carried
// over) so the create/update is clean. Returns addons.ErrNotFound when
// the source secret doesn't exist in the home namespace, so the caller
// can surface "backups not configured" instead of minting a doomed Job.
func (h *BackupsHandler) mirrorBackupSecret(ctx context.Context, ns string) error {
	src, err := h.Kube.Clientset.CoreV1().Secrets(h.Namespace).Get(ctx, backupSecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("%w: %s not found in %s", addons.ErrNotFound, backupSecretName, h.Namespace)
	}
	if err != nil {
		return fmt.Errorf("read source secret: %w", err)
	}
	data := make(map[string][]byte, len(src.Data))
	for k, v := range src.Data {
		data[k] = v
	}
	secrets := h.Kube.Clientset.CoreV1().Secrets(ns)
	if existing, gerr := secrets.Get(ctx, backupSecretName, metav1.GetOptions{}); gerr == nil {
		existing.Data = data
		if _, uerr := secrets.Update(ctx, existing, metav1.UpdateOptions{}); uerr != nil {
			return fmt.Errorf("update mirrored secret: %w", uerr)
		}
		return nil
	} else if !apierrors.IsNotFound(gerr) {
		return fmt.Errorf("check mirrored secret: %w", gerr)
	}
	dst := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      backupSecretName,
			Namespace: ns,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "kuso-server",
				"kuso.sislelabs.com/mirror-of": h.Namespace,
			},
		},
		Type: src.Type,
		Data: data,
	}
	if _, cerr := secrets.Create(ctx, dst, metav1.CreateOptions{}); cerr != nil && !apierrors.IsAlreadyExists(cerr) {
		return fmt.Errorf("create mirrored secret: %w", cerr)
	}
	return nil
}

// s3Client builds an aws-sdk-v2 S3 client from the kuso-backup-s3
// Secret. Returns ErrNotFound when the secret is missing so the
// handler can return 503 with a friendly message.
func (h *BackupsHandler) s3Client(ctx context.Context) (*s3.Client, string, error) {
	sec, err := h.Kube.Clientset.CoreV1().Secrets(h.Namespace).Get(ctx, backupSecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, "", errors.New("backup not configured: PUT /api/admin/backup-settings first")
	}
	if err != nil {
		return nil, "", err
	}
	bucket := string(sec.Data["bucket"])
	endpoint := string(sec.Data["endpoint"])
	region := string(sec.Data["region"])
	akid := string(sec.Data["accessKeyId"])
	skey := string(sec.Data["secretAccessKey"])
	if region == "" {
		region = "auto"
	}
	// The endpoint is admin-supplied and validateWebhookURL only blocks IP
	// literals — a hostname that RESOLVES to a private/metadata address (IMDS
	// 169.254.169.254, RFC1918, .svc) would otherwise be reachable on the
	// AWS SDK's default transport. Dial through the SSRF-safe client, which
	// resolves the host, rejects reserved-range IPs, dials only the
	// validated IP (defeating DNS rebinding between check and dial), and
	// re-validates every redirect hop (S3 region moves redirect, so
	// refusing outright would break legitimate endpoints). Same guard the
	// notification/coolify-import outbound clients use. Timeout 0: the
	// SDK bounds each request with ctx.
	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(akid, skey, "")),
		awsconfig.WithHTTPClient(httpx.SSRFSafeClient(0)),
	)
	if err != nil {
		return nil, "", err
	}
	cli := s3.NewFromConfig(cfg, func(o *s3.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
			o.UsePathStyle = true // Most S3-compatible stores need this
		}
	})
	return cli, bucket, nil
}

func backupCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 15*time.Second)
}

func envFromSecret(name, secretName, key string) corev1.EnvVar {
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

func envFromSecretOptional(name, secretName, key string) corev1.EnvVar {
	opt := true
	return corev1.EnvVar{
		Name: name,
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
				Key:                  key,
				Optional:             &opt,
			},
		},
	}
}

// ---------- SQL browser ---------------------------------------------

// pgConn dials the addon's postgres using credentials from its
// connection Secret (<release>-conn). Returns a *sql.DB the caller
// must Close. Adds a 5s connect timeout so a wedged addon can't hang
// the request.
// validDBName gates the ?database= override: a Postgres identifier,
// nothing else — this string ends up in a DSN, so the gate is what
// keeps the override from becoming an injection surface.
var validDBName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_$]{0,62}$`)

func (h *BackupsHandler) pgConn(ctx context.Context, project, addon, database string) (*sql.DB, error) {
	// addon may arrive as either the short name ("pg") or the
	// fully-qualified CR name ("e2e-test-pg"); both are valid URL
	// args. ownedAddon collapses to the canonical form (so we don't
	// end up looking for "e2e-test-e2e-test-pg-conn"), verifies the
	// CR belongs to this project, and yields the namespace its -conn
	// Secret actually lives in.
	cr, ns, err := h.ownedAddon(ctx, project, addon)
	if err != nil {
		return nil, err
	}
	releaseName := cr.Name
	connSecret := addons.ConnSecretName(releaseName)
	sec, err := h.Kube.Clientset.CoreV1().Secrets(ns).Get(ctx, connSecret, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("addon %s/%s has no -conn secret", project, addon)
	}
	if err != nil {
		return nil, err
	}
	host := releaseName + "-postgresql"
	port := "5432"
	user := "kuso"
	dbName := "kuso"
	if v, ok := sec.Data["POSTGRES_HOST"]; ok && len(v) > 0 {
		host = string(v)
	}
	if v, ok := sec.Data["POSTGRES_PORT"]; ok && len(v) > 0 {
		port = string(v)
	}
	if v, ok := sec.Data["POSTGRES_USER"]; ok && len(v) > 0 {
		user = string(v)
	}
	if v, ok := sec.Data["POSTGRES_DB"]; ok && len(v) > 0 {
		dbName = string(v)
	}
	pass := string(sec.Data["POSTGRES_PASSWORD"])
	if pass == "" {
		return nil, errors.New("addon -conn secret missing POSTGRES_PASSWORD")
	}
	// Optional logical-database override (multi-DB servers: one addon
	// hosting a database per tenant). The conn secret's credentials are
	// the server admin, so they can browse every logical DB.
	if database != "" {
		if !validDBName.MatchString(database) {
			return nil, fmt.Errorf("invalid database name %q", database)
		}
		dbName = database
	}
	// SQL-browser connections use a dedicated NOSUPERUSER login role
	// (kuso_browser) rather than the addon's admin user (kuso, which the
	// stock postgres image makes a superuser). A superuser can run
	// `COPY … TO PROGRAM` (shell exec) and pg_read_file regardless of any
	// query denylist — the browser role can't, because it lacks
	// pg_execute_server_program / pg_read_server_files membership. The
	// role is provisioned lazily on first use (below), granted read+DML
	// on the app schema so the browser still works, and shares the same
	// password. This is the authoritative CRIT-2 fix; blockedSQLBuiltin
	// is defense in depth on top of it.
	//
	// FAIL CLOSED: if the NOSUPERUSER role can't be provisioned, refuse
	// the connection instead of falling back to the addon's admin user.
	//
	// The previous behaviour degraded to the superuser and relied on the
	// statement denylist alone. That's not equivalent: a superuser
	// session can reach `COPY … TO PROGRAM` and pg_read_file, which is
	// shell execution inside the addon pod, and the denylist is a
	// string-matching layer that was only ever meant as defense in depth
	// *on top of* the role. Worse, the degraded path was reachable by
	// making provisioning fail, which turned a provisioning error into a
	// privilege upgrade. A broken SQL browser is a far better outcome
	// than a superuser console.
	if err := h.Addons.EnsureBrowserRole(ctx, ns, releaseName, user, pass, dbName); err != nil {
		h.Logger.Error("sql-browser: could not provision NOSUPERUSER role — refusing to open the browser as the addon superuser",
			"project", project, "addon", addon, "err", err)
		return nil, fmt.Errorf("SQL browser unavailable: the restricted %s role could not be provisioned on this addon (refusing to fall back to the superuser account)",
			addons.BrowserRole)
	}

	db, err := pgOpenAs(host, port, addons.BrowserRole, pass, dbName)
	if err != nil {
		return nil, err
	}
	// Same rule if the role exists but can't authenticate — no silent
	// downgrade to the admin account.
	if perr := db.PingContext(ctx); perr != nil {
		_ = db.Close()
		h.Logger.Error("sql-browser: restricted role unusable — refusing to open the browser as the addon superuser",
			"project", project, "addon", addon, "err", perr)
		return nil, fmt.Errorf("SQL browser unavailable: the restricted %s role could not connect (refusing to fall back to the superuser account)",
			addons.BrowserRole)
	}
	return db, nil
}

// pgOpenAs opens a single-connection *sql.DB to the addon postgres as the
// given user. One connection, no pooling: the request is short and we'd
// rather spend a fresh handshake than risk pool reuse across users.
func pgOpenAs(host, port, user, pass, dbName string) (*sql.DB, error) {
	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable connect_timeout=5",
		host, port, user, pass, dbName)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(0)
	return db, nil
}

// SQLTables returns the schema → table list for the addon's
// database. Filters out pg_catalog + information_schema; users want
// their tables, not the postgres internals.
func (h *BackupsHandler) SQLTables(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	addon := chi.URLParam(r, "addon")
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	// The SQL browser can SELECT any row — including secret-bearing app
	// tables (users.password_hash, tokens, etc.). That's the same class
	// of leak as reading env values, so it's ADMIN-ONLY in role-system v2.
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleViewer) {
		return
	}
	if !callerCanRunSQL(ctx, h.DB, project) {
		writeErr(w, http.StatusForbidden, "forbidden: the SQL browser requires the admin role")
		return
	}

	// ClickHouse: list database.table via system.tables (CH's "schema" is a
	// database). Skip the CH internal databases.
	if info, isCH, cerr := h.clickhouseConnInfo(ctx, project, addon); cerr == nil && isCH {
		res, err := h.chSelect(ctx, info,
			`SELECT database, name FROM system.tables
			 WHERE database NOT IN ('system','INFORMATION_SCHEMA','information_schema')
			 ORDER BY database, name`)
		if err != nil {
			writeErr(w, http.StatusBadGateway, "query: "+err.Error())
			return
		}
		type row struct {
			Schema string `json:"schema"`
			Name   string `json:"name"`
		}
		out := make([]row, 0, len(res.Rows))
		for _, r := range res.Rows {
			if len(r) >= 2 {
				out = append(out, row{Schema: r[0], Name: r[1]})
			}
		}
		writeJSON(w, http.StatusOK, out)
		return
	}

	conn, err := h.pgConn(ctx, project, addon, r.URL.Query().Get("database"))
	if err != nil {
		writeAddonErr(w, err)
		return
	}
	defer conn.Close()

	rows, err := conn.QueryContext(ctx, `
		SELECT table_schema, table_name
		FROM information_schema.tables
		WHERE table_schema NOT IN ('pg_catalog', 'information_schema')
		  AND table_type = 'BASE TABLE'
		ORDER BY table_schema, table_name
	`)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "query: "+err.Error())
		return
	}
	defer rows.Close()
	type row struct {
		Schema string `json:"schema"`
		Name   string `json:"name"`
	}
	out := make([]row, 0, 64)
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.Schema, &r.Name); err != nil {
			continue
		}
		out = append(out, r)
	}
	writeJSON(w, http.StatusOK, out)
}

// sqlCommentRe strips SQL comments (block and line) so they can't be
// used to break up a denied token, e.g. `COPY/**/(...)`. Mirrors the
// ClickHouse path's chCommentRe.
var sqlCommentRe = regexp.MustCompile(`(?s)/\*.*?\*/|--[^\n]*`)

// sqlDeniedTokenRe matches the high-blast-radius Postgres primitives as
// whole tokens (word-boundaried), so `COPY` followed by ANY whitespace,
// a comment, or an open paren is caught — not just `copy ` with a
// literal trailing space (the historical bypass: `COPY\t(SELECT 1) TO
// PROGRAM …` contained no "copy " substring and slipped through).
var sqlDeniedTokenRe = regexp.MustCompile(`(?i)\b(copy|pg_read_file|pg_read_binary_file|pg_ls_dir|pg_stat_file|lo_import|lo_export|lo_from_bytea|dblink|dblink_connect|dblink_exec|pg_logfile_rotate|pg_reload_conf)\b`)

// blockedSQLBuiltin rejects queries that reach Postgres' file / network
// / process primitives, which a read-only TX does NOT stop (a SELECT
// pg_read_file(...) is a read; COPY (SELECT …) TO PROGRAM forks a shell
// but is "read-only" as far as the transaction is concerned). This is
// DEFENSE IN DEPTH — the authoritative barrier is that the browser
// connects as a NOSUPERUSER role without pg_execute_server_program /
// pg_read_server_files membership (see addon chart), so COPY … TO/FROM
// PROGRAM and pg_read_file are denied by Postgres regardless. This
// filter is the belt to that role's braces.
//
// We normalize before matching: strip comments, then token-match on word
// boundaries. A bare substring scan (the previous implementation) was
// evadable with a tab/newline/comment/paren after the keyword.
func blockedSQLBuiltin(q string) string {
	scrubbed := sqlCommentRe.ReplaceAllString(q, " ")
	if m := sqlDeniedTokenRe.FindString(scrubbed); m != "" {
		switch strings.ToLower(m) {
		case "copy":
			return "COPY (filesystem / TO PROGRAM / outbound)"
		case "dblink", "dblink_connect", "dblink_exec":
			return "outbound network (dblink)"
		case "lo_import", "lo_export", "lo_from_bytea":
			return "large-object filesystem access"
		case "pg_logfile_rotate", "pg_reload_conf":
			return "server-control function"
		default:
			return "filesystem access (" + strings.ToLower(m) + ")"
		}
	}
	return ""
}

// SQLQueryRequest is the body of POST /sql/query. We accept a raw
// SQL string but enforce read-only at runtime, not on the parsed
// statement — that's defence in depth against drivers that quietly
// support multi-statement strings.
type SQLQueryRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

// SQLQueryResponse is intentionally simple: a string column header
// list + rows of strings. We render numbers/dates as their pg
// text representation so the client doesn't have to deal with
// driver type ambiguity. Trade-off: bigints lose precision in JSON,
// but the user is browsing, not aggregating.
type SQLQueryResponse struct {
	Columns   []string   `json:"columns"`
	Rows      [][]string `json:"rows"`
	Truncated bool       `json:"truncated"`
	Elapsed   string     `json:"elapsed"`
}

func (h *BackupsHandler) SQLQuery(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	addon := chi.URLParam(r, "addon")

	var req SQLQueryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if req.Query == "" {
		writeErr(w, http.StatusBadRequest, "query required")
		return
	}
	limit := req.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	// Admin-only — read-only is enforced inside the tx, but a
	// SELECT on `users.password_hash` is plenty bad on its own. Same
	// secret-bearing-read boundary as env values / shell in v2.
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleViewer) {
		return
	}
	if !callerCanRunSQL(ctx, h.DB, project) {
		writeErr(w, http.StatusForbidden, "forbidden: the SQL browser requires the admin role")
		return
	}

	// ClickHouse addons don't use the Postgres path (no read-only tx /
	// information_schema). Detect by the conn secret and run over the CH HTTP
	// interface with readonly mode + a CH-specific builtin denylist.
	if info, isCH, cerr := h.clickhouseConnInfo(ctx, project, addon); cerr == nil && isCH {
		if reason := blockedClickHouseBuiltin(req.Query); reason != "" {
			writeErr(w, http.StatusForbidden, "query rejected: "+reason)
			return
		}
		h.auditSQLQuery(ctx, r, project, addon, req.Query)
		start := time.Now()
		out, status, err := h.runClickHouseQuery(ctx, info, req.Query, limit)
		if err != nil {
			writeErr(w, status, err.Error())
			return
		}
		out.Elapsed = time.Since(start).Round(time.Millisecond).String()
		writeJSON(w, http.StatusOK, out)
		return
	}

	// Reject queries that hit Postgres' file/network/process built-ins.
	// Read-only TX prevents writes but pg_read_file / pg_ls_dir /
	// dblink / lo_import etc. are all SELECT-shaped on a misconfigured
	// server (superuser DB user, or grants the operator forgot). Cheap
	// substring scan; defence-in-depth on top of role privileges.
	if reason := blockedSQLBuiltin(req.Query); reason != "" {
		writeErr(w, http.StatusForbidden, "query rejected: "+reason)
		return
	}
	conn, err := h.pgConn(ctx, project, addon, r.URL.Query().Get("database"))
	if err != nil {
		writeAddonErr(w, err)
		return
	}
	defer conn.Close()

	// Open a read-only transaction with a statement timeout. If
	// anything in the user's query tries to write, postgres rejects
	// it inside this transaction — no need to parse the SQL ourselves.
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		writeErr(w, http.StatusBadGateway, "begin: "+err.Error())
		return
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SET LOCAL statement_timeout = '5s'"); err != nil {
		writeErr(w, http.StatusBadGateway, "set timeout: "+err.Error())
		return
	}

	start := time.Now()
	h.auditSQLQuery(ctx, r, project, addon, req.Query)
	rows, err := tx.QueryContext(ctx, req.Query)
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		writeErr(w, http.StatusBadGateway, "columns: "+err.Error())
		return
	}
	out := SQLQueryResponse{Columns: cols, Rows: make([][]string, 0, limit)}
	for rows.Next() {
		if len(out.Rows) >= limit {
			out.Truncated = true
			break
		}
		raw := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range raw {
			ptrs[i] = &raw[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			continue
		}
		row := make([]string, len(cols))
		for i, v := range raw {
			row[i] = stringifyCell(v)
		}
		out.Rows = append(out.Rows, row)
	}
	out.Elapsed = time.Since(start).Round(time.Millisecond).String()
	writeJSON(w, http.StatusOK, out)
}

// auditSQLQuery records a raw SQL-runner invocation. The SQL browser is the
// highest-blast-radius read endpoint we expose — admin gates access, but a
// single compromised admin credential reading every row of `users` should
// leave a trail. Shared by the Postgres and ClickHouse query paths.
func (h *BackupsHandler) auditSQLQuery(ctx context.Context, r *http.Request, project, addon, query string) {
	if h.Audit == nil {
		return
	}
	uid := ""
	if c, ok := auth.ClaimsFromContext(r.Context()); ok && c != nil {
		uid = c.UserID
	}
	// Truncate so a multi-MB user-pasted query doesn't blow up the audit row.
	// 4 KiB is plenty for forensics. Append sha256(full) so "did this exact
	// query run twice" stays answerable even after truncation.
	q := query
	sum := sha256.Sum256([]byte(q))
	hash := hex.EncodeToString(sum[:])[:16]
	if len(q) > 4096 {
		q = q[:4096] + "…[truncated]"
	}
	h.Audit.Log(ctx, audit.Entry{
		User:     uid,
		Severity: "info",
		Action:   "addon.sql_query",
		Pipeline: project,
		App:      addon,
		Resource: "kusoaddon",
		Message:  fmt.Sprintf("[sha256:%s] %s", hash, q),
	})
}

// stringifyCell turns whatever the pg driver returned into a JSON-
// safe string. We don't try to preserve numeric types — the client
// is rendering a table, not feeding the value into a calculation.
func stringifyCell(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case []byte:
		return string(x)
	case string:
		return x
	case time.Time:
		return x.UTC().Format(time.RFC3339)
	default:
		return fmt.Sprintf("%v", x)
	}
}
