package handlers

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/addons"
	"kuso/server/internal/db"
)

// Download streams a fresh, on-demand dump of the addon's data straight
// to the caller — independent of the S3-scheduled-backup config. It works
// precisely when GET .../backups returns 503 "backup not configured",
// which is the whole point: it's the escape hatch that gets your data out
// even when nobody wired up an S3 bucket.
//
// Dispatches on the addon's spec.kind:
//
//   - postgres — in-process `pg_dump` (the kuso-server image bundles
//     postgresql-client; see Dockerfile) → gzip → attachment. Copies the
//     control-plane /api/admin/backup pattern (backup.go:Download) but
//     points at the addon's <release>-conn Secret instead of
//     kuso-postgres-conn.
//   - s3 — list the bucket and stream every object into one gzipped tar.
//
// Any other kind (redis, clickhouse, redpanda) → 400. Their dump tooling
// differs and is out of scope.
//
// Both paths STREAM: nothing is buffered whole in RAM, so a 50 GB dataset
// won't OOM the server. If the source dies mid-stream, the gzip is
// truncated and the client errors on decompress — preferable to silently
// shipping a half-dump (same tradeoff the control-plane handler documents).
//
// Admin-gated (secrets:read): it exfiltrates the ENTIRE dataset — every
// row of every table, including secret-bearing app tables (password
// hashes, session tokens, API keys). That's a strict superset of the
// SQL browser (callerCanRunSQL) and project Export (callerCanReadSecrets),
// both admin-only, so it takes the same admin secret-read bar. Restore's
// editor bar is the wrong analogy: Restore *writes* and returns no data;
// Download reads the whole dataset out to the caller.
func (h *BackupsHandler) Download(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	addon := chi.URLParam(r, "addon")

	// A dump/mirror of a large dataset can run for minutes — override the
	// short backupCtx (15s) used by the metadata-only List/Restore paths.
	// 5-minute default with a ?timeout= override capped at 1h, mirroring
	// the control-plane /api/admin/backup handler.
	timeout := 5 * time.Minute
	if v := r.URL.Query().Get("timeout"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 && d < time.Hour {
			timeout = d
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	// Admin secret-read — Download returns the entire dataset (including
	// secret-bearing app tables) to the caller, the same blast radius as
	// the SQL browser and project Export, both admin-only. requireProjectAccess
	// first so a non-member gets a plain 403 before we resolve the addon.
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleViewer) {
		return
	}
	if !callerCanReadSecrets(ctx, h.DB, project) {
		http.Error(w, "forbidden: downloading a database dump (which contains secret-bearing rows) requires the admin role", http.StatusForbidden)
		return
	}

	// Ownership-checked resolution + the project's execution namespace
	// (where the -conn Secret lives) — see ownedAddon in backups.go.
	cr, ns, err := h.ownedAddon(ctx, project, addon)
	if errors.Is(err, addons.ErrNotFound) {
		http.Error(w, fmt.Sprintf("addon %s/%s not found", project, addon), http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "resolve addon: "+err.Error(), http.StatusInternalServerError)
		return
	}
	releaseName := cr.Name

	stamp := time.Now().UTC().Format("20060102T150405Z")
	switch cr.Spec.Kind {
	case "postgres":
		h.downloadPostgres(ctx, w, project, addon, ns, releaseName, stamp)
	case "s3", "minio":
		h.downloadS3(ctx, w, project, addon, ns, releaseName, stamp)
	default:
		http.Error(w,
			fmt.Sprintf("direct download not supported for %s addons (postgres and s3 only)", cr.Spec.Kind),
			http.StatusBadRequest)
	}
}

// downloadPostgres runs pg_dump against the addon's DB and streams the
// gzipped SQL. Version skew: the bundled client is postgresql16-client,
// so pg_dump handles PG <=16. A future PG-17+ addon would make pg_dump
// refuse with "server version too new" — the client sees a failed
// download (truncated gzip), not a corrupt one. Bump the Dockerfile's
// client version when we ship a newer Postgres addon.
func (h *BackupsHandler) downloadPostgres(ctx context.Context, w http.ResponseWriter, project, addon, ns, releaseName, stamp string) {
	dsn, err := h.addonDSN(ctx, ns, releaseName)
	if err != nil {
		http.Error(w, "backup unavailable: "+err.Error(), http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s-%s-%s.sql.gz"`, project, addon, stamp))

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
		h.Logger.Error("addon backup: copy", "project", project, "addon", addon, "err", err)
		_ = cmd.Process.Kill()
		_ = gz.Close()
		return
	}
	if err := cmd.Wait(); err != nil {
		// Body is already mid-stream, so we can't change the response
		// code now. Log stderr for diagnosis; the client will see a
		// truncated gzip and error on decompress.
		var buf strings.Builder
		if stderr != nil {
			_, _ = io.Copy(&buf, stderr)
		}
		h.Logger.Error("addon backup: pg_dump wait",
			"project", project, "addon", addon, "err", err, "stderr", buf.String())
	}
	_ = gz.Close()
}

// downloadS3 lists the addon's bucket and streams every object into a
// single gzipped tar. The addon's storage endpoint is in-cluster
// (http://<release>-storage:9000) and reachable from kuso-server.
func (h *BackupsHandler) downloadS3(ctx context.Context, w http.ResponseWriter, project, addon, ns, releaseName, stamp string) {
	cli, bucket, err := h.addonS3Client(ctx, ns, releaseName)
	if err != nil {
		http.Error(w, "backup unavailable: "+err.Error(), http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s-%s-%s.tar.gz"`, project, addon, stamp))

	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)

	paginator := s3.NewListObjectsV2Paginator(cli, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			// Mid-stream: can't change the status code. Close what we've
			// written (truncated tar) and let the client error.
			h.Logger.Error("addon backup: s3 list", "project", project, "addon", addon, "err", err)
			_ = tw.Close()
			_ = gz.Close()
			return
		}
		for _, obj := range page.Contents {
			key := aws.ToString(obj.Key)
			if err := h.streamS3Object(ctx, cli, tw, bucket, key, aws.ToInt64(obj.Size), obj.LastModified); err != nil {
				h.Logger.Error("addon backup: s3 object",
					"project", project, "addon", addon, "key", key, "err", err)
				_ = tw.Close()
				_ = gz.Close()
				return
			}
		}
	}
	if err := tw.Close(); err != nil {
		h.Logger.Error("addon backup: tar close", "project", project, "addon", addon, "err", err)
	}
	_ = gz.Close()
}

// streamS3Object copies one object body into the tar writer. Body is
// closed before return so a large bucket doesn't leak connections.
func (h *BackupsHandler) streamS3Object(ctx context.Context, cli *s3.Client, tw *tar.Writer, bucket, key string, size int64, mod *time.Time) error {
	out, err := cli.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("get %s: %w", key, err)
	}
	defer out.Body.Close()

	hdr := &tar.Header{
		Name: key,
		Mode: 0o644,
		Size: size,
	}
	if mod != nil {
		hdr.ModTime = *mod
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("tar header %s: %w", key, err)
	}
	if _, err := io.Copy(tw, out.Body); err != nil {
		return fmt.Errorf("copy %s: %w", key, err)
	}
	return nil
}

// addonDSN resolves a libpq DSN string for pg_dump from the addon's
// <release>-conn Secret. Sibling of pgConn (backups.go), which returns a
// *sql.DB; pg_dump wants the DSN as a CLI arg instead.
func (h *BackupsHandler) addonDSN(ctx context.Context, ns, releaseName string) (string, error) {
	connSecret := addons.ConnSecretName(releaseName)
	sec, err := h.Kube.Clientset.CoreV1().Secrets(ns).Get(ctx, connSecret, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", fmt.Errorf("addon has no -conn secret")
	}
	if err != nil {
		return "", err
	}
	host := releaseName + "-postgresql"
	port := "5432"
	user := "kuso"
	dbName := "kuso"
	if v := sec.Data["POSTGRES_HOST"]; len(v) > 0 {
		host = string(v)
	}
	if v := sec.Data["POSTGRES_PORT"]; len(v) > 0 {
		port = string(v)
	}
	if v := sec.Data["POSTGRES_USER"]; len(v) > 0 {
		user = string(v)
	}
	if v := sec.Data["POSTGRES_DB"]; len(v) > 0 {
		dbName = string(v)
	}
	pass := string(sec.Data["POSTGRES_PASSWORD"])
	if pass == "" {
		return "", fmt.Errorf("addon -conn secret missing POSTGRES_PASSWORD")
	}
	// URL form so pg_dump takes it as a single connection-string arg.
	// sslmode=disable + connect_timeout match pgConn's in-cluster dial.
	return fmt.Sprintf("postgresql://%s:%s@%s:%s/%s?sslmode=disable&connect_timeout=5",
		user, pass, host, port, dbName), nil
}

// addonS3Client builds an S3 client from the addon's own <release>-conn
// Secret (S3_ENDPOINT/BUCKET/ACCESS_KEY_ID/SECRET_ACCESS_KEY). Distinct
// from s3Client (backups.go), which reads the cluster-wide kuso-backup-s3
// Secret — this one targets the addon's storage itself.
func (h *BackupsHandler) addonS3Client(ctx context.Context, ns, releaseName string) (*s3.Client, string, error) {
	connSecret := addons.ConnSecretName(releaseName)
	sec, err := h.Kube.Clientset.CoreV1().Secrets(ns).Get(ctx, connSecret, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, "", fmt.Errorf("addon has no -conn secret")
	}
	if err != nil {
		return nil, "", err
	}
	bucket := string(sec.Data["S3_BUCKET"])
	endpoint := string(sec.Data["S3_ENDPOINT"])
	region := string(sec.Data["S3_REGION"])
	akid := string(sec.Data["S3_ACCESS_KEY_ID"])
	skey := string(sec.Data["S3_SECRET_ACCESS_KEY"])
	if bucket == "" || akid == "" || skey == "" {
		return nil, "", fmt.Errorf("addon -conn secret missing S3 credentials")
	}
	if region == "" {
		region = "us-east-1"
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
			o.UsePathStyle = true // MinIO + most S3-compatible stores need this
		}
	})
	return cli, bucket, nil
}
