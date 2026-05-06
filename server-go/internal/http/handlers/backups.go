package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
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
	"kuso/server/internal/db"
	"kuso/server/internal/kube"
)

// BackupsHandler exposes /api/admin/backup-settings (CRUD on the
// kuso-backup-s3 Secret) and /api/projects/{p}/addons/{a}/backups
// (list + restore, gated on the same Secret existing).
type BackupsHandler struct {
	Kube      *kube.Client
	DB        *db.DB
	Namespace string
	Logger    *slog.Logger
}

const backupSecretName = "kuso-backup-s3"

func (h *BackupsHandler) Mount(r chi.Router) {
	r.Get("/api/admin/backup-settings", h.GetSettings)
	r.Put("/api/admin/backup-settings", h.PutSettings)
	r.Get("/api/projects/{project}/addons/{addon}/backups", h.List)
	r.Post("/api/projects/{project}/addons/{addon}/backups/restore", h.Restore)
	// SQL browser: list tables + run a read-only SELECT against the
	// addon's postgres. The query is wrapped in a read-only
	// transaction with a statement_timeout — defence in depth against
	// a stray `; DROP TABLE` or a runaway scan.
	r.Get("/api/projects/{project}/addons/{addon}/sql/tables", h.SQLTables)
	r.Post("/api/projects/{project}/addons/{addon}/sql/query", h.SQLQuery)
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
		http.Error(w, "internal", http.StatusInternalServerError)
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
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.Bucket == "" || req.Endpoint == "" || req.AccessKeyID == "" {
		http.Error(w, "bucket, endpoint, accessKeyId required", http.StatusBadRequest)
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
		http.Error(w, "secretAccessKey required on first save", http.StatusBadRequest)
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
			http.Error(w, "internal", http.StatusInternalServerError)
			return
		}
	} else {
		sec.ResourceVersion = existing.ResourceVersion
		_, err := h.Kube.Clientset.CoreV1().Secrets(h.Namespace).Update(ctx, sec, metav1.UpdateOptions{})
		if err != nil {
			h.Logger.Error("backup: update secret", "err", err)
			http.Error(w, "internal", http.StatusInternalServerError)
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

func (h *BackupsHandler) List(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	addon := chi.URLParam(r, "addon")
	ctx, cancel := backupCtx(r)
	defer cancel()
	// Restoring/listing/querying an addon's data needs project Owner —
	// not Deployer, since SQL access can read passwords / PII / billing
	// records that even a deploy-permissioned teammate shouldn't see.
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleOwner) {
		return
	}

	cli, bucket, err := h.s3Client(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	// CronJob uploads to s3://bucket/<project>/<addon-fqn>/<ts>.sql.gz
	// where <addon-fqn> = the helm release name = "<project>-<short>".
	// Callers pass either the FQN (UI / canvas use metadata.name) or
	// the short name (CLI ergonomic flow). Normalise to FQN so the
	// prefix matches what the cronjob actually wrote — and so a
	// caller that passes the short name doesn't list zero objects
	// because of a project-prefix mismatch.
	addonFQN := addons.CRName(project, addon)
	prefix := project + "/" + addonFQN + "/"
	out, err := cli.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
		Prefix: aws.String(prefix),
	})
	if err != nil {
		h.Logger.Error("backup: list", "err", err)
		http.Error(w, "list failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	items := make([]BackupObject, 0, len(out.Contents))
	for _, o := range out.Contents {
		key := aws.ToString(o.Key)
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
		http.Error(w, "key required", http.StatusBadRequest)
		return
	}
	ctx, cancel := backupCtx(r)
	defer cancel()
	// Owner-only — Restore overwrites the live DB with a snapshot
	// (destructive) and a Deployer-level user shouldn't be able to
	// roll back data without the project owner signing off.
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleOwner) {
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
	releaseName := addons.CRName(project, destAddon)
	jobName := fmt.Sprintf("%s-restore-%d", releaseName, time.Now().Unix())

	one := int32(1)
	zero := int32(0)
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: h.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by":      "kuso-server",
				"kuso.sislelabs.com/role":           "restore",
				"kuso.sislelabs.com/project":        project,
				"kuso.sislelabs.com/addon":          destAddon,
				"kuso.sislelabs.com/source-addon":   addon,
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
						Args: []string{`
set -e
echo "==> downloading s3://${BUCKET}/${KEY}"
aws s3 cp --endpoint-url "${S3_ENDPOINT}" "s3://${BUCKET}/${KEY}" /tmp/dump.sql.gz
echo "==> piping into psql"
gunzip -c /tmp/dump.sql.gz | PGPASSWORD="${POSTGRES_PASSWORD}" psql \
  -h "${POSTGRES_HOST}" -U "${POSTGRES_USER}" "${POSTGRES_DB}"
echo "==> done"
`},
						Env: []corev1.EnvVar{
							{Name: "KEY", Value: req.Key},
							// Source ALL connection parameters from the
							// addon's <release>-conn Secret. Hard-coding
							// host/user/db here was the v0.7.51 bug:
							// host had a stale "-postgresql" suffix
							// (left over from when the chart used the
							// bitnami subchart's naming) and db
							// defaulted to "kuso" but the chart actually
							// uses "<project>". Reading from the conn
							// secret means the restore Job tracks
							// whatever the chart wrote — same source of
							// truth as the application pods.
							envFromSecret("POSTGRES_HOST", releaseName+"-conn", "POSTGRES_HOST"),
							envFromSecret("POSTGRES_USER", releaseName+"-conn", "POSTGRES_USER"),
							envFromSecret("POSTGRES_DB", releaseName+"-conn", "POSTGRES_DB"),
							envFromSecret("POSTGRES_PASSWORD", releaseName+"-conn", "POSTGRES_PASSWORD"),
							envFromSecret("BUCKET", backupSecretName, "bucket"),
							envFromSecret("S3_ENDPOINT", backupSecretName, "endpoint"),
							envFromSecret("AWS_ACCESS_KEY_ID", backupSecretName, "accessKeyId"),
							envFromSecret("AWS_SECRET_ACCESS_KEY", backupSecretName, "secretAccessKey"),
							envFromSecretOptional("AWS_DEFAULT_REGION", backupSecretName, "region"),
						},
					}},
				},
			},
		},
	}
	created, err := h.Kube.Clientset.BatchV1().Jobs(h.Namespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		h.Logger.Error("backup: create restore job", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"job": created.Name})
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
	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(akid, skey, "")),
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
func (h *BackupsHandler) pgConn(ctx context.Context, project, addon string) (*sql.DB, error) {
	// addon may arrive as either the short name ("pg") or the
	// fully-qualified CR name ("e2e-test-pg"); both are valid URL
	// args. addons.CRName collapses to the canonical form so we
	// don't end up looking for "e2e-test-e2e-test-pg-conn".
	releaseName := addons.CRName(project, addon)
	connSecret := addons.ConnSecretName(releaseName)
	sec, err := h.Kube.Clientset.CoreV1().Secrets(h.Namespace).Get(ctx, connSecret, metav1.GetOptions{})
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
	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable connect_timeout=5",
		host, port, user, pass, dbName)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	// One per-request connection, no pooling: the request is short
	// and we'd rather spend a fresh handshake than risk pool reuse
	// across users.
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
	// SQL browser reaches the user's data — Owner-only.
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleOwner) {
		return
	}

	conn, err := h.pgConn(ctx, project, addon)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
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
		http.Error(w, "query: "+err.Error(), http.StatusBadGateway)
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
	Columns  []string   `json:"columns"`
	Rows     [][]string `json:"rows"`
	Truncated bool      `json:"truncated"`
	Elapsed  string     `json:"elapsed"`
}

func (h *BackupsHandler) SQLQuery(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	addon := chi.URLParam(r, "addon")

	var req SQLQueryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.Query == "" {
		http.Error(w, "query required", http.StatusBadRequest)
		return
	}
	limit := req.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	// Owner-only — read-only is enforced inside the tx, but a
	// SELECT on `users.password_hash` is plenty bad on its own.
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleOwner) {
		return
	}
	conn, err := h.pgConn(ctx, project, addon)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer conn.Close()

	// Open a read-only transaction with a statement timeout. If
	// anything in the user's query tries to write, postgres rejects
	// it inside this transaction — no need to parse the SQL ourselves.
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		http.Error(w, "begin: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SET LOCAL statement_timeout = '5s'"); err != nil {
		http.Error(w, "set timeout: "+err.Error(), http.StatusBadGateway)
		return
	}

	start := time.Now()
	rows, err := tx.QueryContext(ctx, req.Query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		http.Error(w, "columns: "+err.Error(), http.StatusBadGateway)
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
