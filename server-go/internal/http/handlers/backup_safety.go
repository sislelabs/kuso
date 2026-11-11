package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/audit"
	"kuso/server/internal/kube"
)

// backupInsecureHTTPKey in kuso-backup-s3 records that an admin accepted a
// plain-http endpoint (BLOB-7).
const backupInsecureHTTPKey = "allowInsecureHttp"

// checkBackupEndpointScheme refuses http:// unless allowed. SigV4 signs a
// request but does not encrypt it: every dump and the requests carrying
// it would cross the network in clear.
func checkBackupEndpointScheme(endpoint string, allow bool) (insecure bool, err error) {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return false, fmt.Errorf("invalid URL: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "http") {
		return false, nil
	}
	if !allow {
		return false, errors.New("plain http sends every backup unencrypted over the network; use https, or set allowInsecureHttp to accept that")
	}
	return true, nil
}

// BackupBucketSafety is what the backup health page shows about the
// bucket itself (BLOB-4, BLOB-7).
type BackupBucketSafety struct {
	Bucket           string `json:"bucket"`
	InsecureEndpoint bool   `json:"insecureEndpoint"`
	// Versioning: Enabled | Suspended | Off | unknown.
	Versioning string `json:"versioning"`
	// ObjectLock: Enabled | Off | unknown.
	ObjectLock string `json:"objectLock"`
	// ScopedProjects have their own prefix-limited key; every other
	// project's backup pods use the shared one.
	ScopedProjects []string `json:"scopedProjects"`
	Warnings       []string `json:"warnings,omitempty"`
}

type bucketStateReader interface {
	GetBucketVersioning(ctx context.Context, in *s3.GetBucketVersioningInput, opts ...func(*s3.Options)) (*s3.GetBucketVersioningOutput, error)
	GetObjectLockConfiguration(ctx context.Context, in *s3.GetObjectLockConfigurationInput, opts ...func(*s3.Options)) (*s3.GetObjectLockConfigurationOutput, error)
}

// readBucketProtection reports whether anything stops a credential holder
// from destroying dumps: versioning keeps overwritten/deleted objects,
// object lock makes them immutable. Read errors report "unknown".
func readBucketProtection(ctx context.Context, cli bucketStateReader, bucket string, out *BackupBucketSafety) {
	out.Versioning, out.ObjectLock = "unknown", "unknown"
	if v, err := cli.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: aws.String(bucket)}); err != nil {
		out.Warnings = append(out.Warnings, "could not read bucket versioning: "+err.Error())
	} else if v.Status == "" {
		out.Versioning = "Off"
	} else {
		out.Versioning = string(v.Status)
	}
	l, err := cli.GetObjectLockConfiguration(ctx, &s3.GetObjectLockConfigurationInput{Bucket: aws.String(bucket)})
	switch {
	case err != nil && strings.Contains(err.Error(), "ObjectLockConfigurationNotFound"):
		out.ObjectLock = "Off"
	case err != nil:
		out.Warnings = append(out.Warnings, "could not read object lock: "+err.Error())
	case l.ObjectLockConfiguration != nil && l.ObjectLockConfiguration.ObjectLockEnabled == s3types.ObjectLockEnabledEnabled:
		out.ObjectLock = "Enabled"
	default:
		out.ObjectLock = "Off"
	}
	if out.Versioning != string(s3types.BucketVersioningStatusEnabled) && out.ObjectLock != "Enabled" &&
		out.Versioning != "unknown" && out.ObjectLock != "unknown" {
		out.Warnings = append(out.Warnings, "nothing protects backups from deletion: anyone holding a backup key can delete every dump it can reach. "+
			"Enable bucket versioning (with a lifecycle rule expiring noncurrent versions) or object lock.")
	}
}

// computeBackupBucketSafety is nil when backups are not configured.
func computeBackupBucketSafety(ctx context.Context, kc *kube.Client, ns string) *BackupBucketSafety {
	if kc == nil || kc.Clientset == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	sec, err := kc.Clientset.CoreV1().Secrets(ns).Get(ctx, backupSecretName, metav1.GetOptions{})
	if err != nil {
		return nil
	}
	out := &BackupBucketSafety{Bucket: string(sec.Data["bucket"]), ScopedProjects: []string{}}
	if u, perr := url.Parse(string(sec.Data["endpoint"])); perr == nil && strings.EqualFold(u.Scheme, "http") {
		out.InsecureEndpoint = true
		out.Warnings = append(out.Warnings, "the backup endpoint is plain http: dumps travel unencrypted")
	}
	if scoped, serr := kc.ListScopedBackupCredentials(ctx); serr == nil {
		for _, s := range scoped {
			out.ScopedProjects = append(out.ScopedProjects, s.Project)
		}
	}
	cli, err := newBackupS3Client(ctx, string(sec.Data["endpoint"]), string(sec.Data["region"]),
		string(sec.Data["accessKeyId"]), string(sec.Data["secretAccessKey"]))
	if err != nil {
		out.Versioning, out.ObjectLock = "unknown", "unknown"
		out.Warnings = append(out.Warnings, "could not build an S3 client: "+err.Error())
		return out
	}
	readBucketProtection(ctx, cli, out.Bucket, out)
	return out
}

// backupBucketReachable checks that the instance backup key can reach
// bucket (BLOB-10). Nil when backups are not configured: there is nothing
// to write with yet.
func backupBucketReachable(ctx context.Context, kc *kube.Client, ns, bucket string) error {
	sec, err := kc.Clientset.CoreV1().Secrets(ns).Get(ctx, backupSecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	cli, err := newBackupS3Client(ctx, string(sec.Data["endpoint"]), string(sec.Data["region"]),
		string(sec.Data["accessKeyId"]), string(sec.Data["secretAccessKey"]))
	if err != nil {
		return err
	}
	if _, err := cli.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)}); err != nil {
		return fmt.Errorf("the instance backup key cannot reach bucket %q: %w", bucket, err)
	}
	return nil
}

// ScopedBackupCredentialRequest is PUT /api/admin/backup-settings/projects/{project}.
type ScopedBackupCredentialRequest struct {
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
}

// scopedCredentialProbe is the S3 surface the scope check needs.
type scopedCredentialProbe interface {
	PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	DeleteObject(ctx context.Context, in *s3.DeleteObjectInput, opts ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, opts ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

// verifyScopedCredential proves a project key can do what a backup run
// does under <project>/ (write, list, delete) and cannot write or list
// outside it. A key that reaches the whole bucket would only rename the
// shared credential's problem.
func verifyScopedCredential(ctx context.Context, cli scopedCredentialProbe, bucket, project string) error {
	in := project + "/.kuso-credential-check"
	if _, err := cli.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(in), Body: strings.NewReader("ok")}); err != nil {
		return fmt.Errorf("cannot write under %s/: %w", project, err)
	}
	if _, err := cli.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket), Prefix: aws.String(project + "/"), MaxKeys: aws.Int32(1)}); err != nil {
		return fmt.Errorf("cannot list under %s/: %w", project, err)
	}
	if _, err := cli.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(in)}); err != nil {
		return fmt.Errorf("cannot delete under %s/ (retention pruning needs it): %w", project, err)
	}
	out := ".kuso-scope-check-" + project
	if _, err := cli.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(out), Body: strings.NewReader("x")}); err == nil {
		_, _ = cli.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(out)})
		return fmt.Errorf("the key can write outside %s/; limit it to that prefix (see the policy endpoint)", project)
	}
	if _, err := cli.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket), MaxKeys: aws.Int32(1)}); err == nil {
		return fmt.Errorf("the key can list the whole bucket; limit listing to the %s/ prefix (see the policy endpoint)", project)
	}
	return nil
}

// scopedBackupPolicy is an AWS/MinIO policy limiting a key to project's
// prefix in bucket: what a backup run and its retention prune need.
func scopedBackupPolicy(bucket, project string) map[string]any {
	return map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{
			{
				"Effect":    "Allow",
				"Action":    []string{"s3:ListBucket"},
				"Resource":  []string{"arn:aws:s3:::" + bucket},
				"Condition": map[string]any{"StringLike": map[string]any{"s3:prefix": []string{project + "/*"}}},
			},
			{
				"Effect":   "Allow",
				"Action":   []string{"s3:GetObject", "s3:PutObject", "s3:DeleteObject", "s3:AbortMultipartUpload", "s3:ListMultipartUploadParts"},
				"Resource": []string{"arn:aws:s3:::" + bucket + "/" + project + "/*"},
			},
		},
	}
}

func (h *BackupsHandler) requireProjectExists(ctx context.Context, w http.ResponseWriter, project string) bool {
	if _, err := h.Kube.GetKusoProject(ctx, h.Namespace, project); err != nil {
		if apierrors.IsNotFound(err) {
			writeErr(w, http.StatusNotFound, "project "+project+" not found")
		} else {
			writeErr(w, http.StatusBadGateway, err.Error())
		}
		return false
	}
	return true
}

// ListScopedCredentials is GET /api/admin/backup-settings/projects.
func (h *BackupsHandler) ListScopedCredentials(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	ctx, cancel := backupCtx(r)
	defer cancel()
	out, err := h.Kube.ListScopedBackupCredentials(ctx)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if out == nil {
		out = []kube.ScopedBackupCredential{}
	}
	writeJSON(w, http.StatusOK, out)
}

// ScopedCredentialPolicy is GET /api/admin/backup-settings/projects/{project}/policy.
func (h *BackupsHandler) ScopedCredentialPolicy(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	project := chi.URLParam(r, "project")
	ctx, cancel := backupCtx(r)
	defer cancel()
	if !h.requireProjectExists(ctx, w, project) {
		return
	}
	sec, err := h.Kube.Clientset.CoreV1().Secrets(h.Namespace).Get(ctx, backupSecretName, metav1.GetOptions{})
	if err != nil {
		writeErr(w, http.StatusPreconditionFailed, "backups not configured: set backup settings first")
		return
	}
	writeJSON(w, http.StatusOK, scopedBackupPolicy(string(sec.Data["bucket"]), project))
}

// PutScopedCredential is PUT /api/admin/backup-settings/projects/{project}.
// The key is verified against the bucket before it is stored.
func (h *BackupsHandler) PutScopedCredential(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	project := chi.URLParam(r, "project")
	var req ScopedBackupCredentialRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.AccessKeyID == "" || req.SecretAccessKey == "" {
		writeErr(w, http.StatusBadRequest, "accessKeyId and secretAccessKey required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	if !h.requireProjectExists(ctx, w, project) {
		return
	}
	sec, err := h.Kube.Clientset.CoreV1().Secrets(h.Namespace).Get(ctx, backupSecretName, metav1.GetOptions{})
	if err != nil {
		writeErr(w, http.StatusPreconditionFailed, "backups not configured: set backup settings first")
		return
	}
	cli, err := newBackupS3Client(ctx, string(sec.Data["endpoint"]), string(sec.Data["region"]), req.AccessKeyID, req.SecretAccessKey)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := verifyScopedCredential(ctx, cli, string(sec.Data["bucket"]), project); err != nil {
		writeErr(w, http.StatusBadRequest, "key rejected: "+err.Error())
		return
	}
	if err := h.Kube.SetScopedBackupCredential(ctx, project, req.AccessKeyID, req.SecretAccessKey); err != nil {
		h.Logger.Error("backup: store scoped credential", "project", project, "err", err)
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	h.Audit.Log(ctx, audit.Entry{
		User: auditUser(ctx), Severity: "critical", Action: "backup.scoped_credential",
		Pipeline: project, Resource: kube.BackupScopedSecretName,
		Message: "project backup key set to " + req.AccessKeyID,
	})
	writeJSON(w, http.StatusOK, map[string]string{
		"secret":  kube.ProjectBackupSecretName(project),
		"message": "the project's backup CronJobs switch to this key on their next reconcile (within 30 minutes)",
	})
}

// DeleteScopedCredential is DELETE /api/admin/backup-settings/projects/{project}:
// the project goes back to the shared key.
func (h *BackupsHandler) DeleteScopedCredential(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	project := chi.URLParam(r, "project")
	ctx, cancel := backupCtx(r)
	defer cancel()
	if err := h.Kube.SetScopedBackupCredential(ctx, project, "", ""); err != nil && !errors.Is(err, kube.ErrBackupNotConfigured) {
		h.Logger.Error("backup: drop scoped credential", "project", project, "err", err)
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	h.Audit.Log(ctx, audit.Entry{
		User: auditUser(ctx), Severity: "critical", Action: "backup.scoped_credential",
		Pipeline: project, Resource: kube.BackupScopedSecretName,
		Message: "project backup key removed; back to the shared key",
	})
	w.WriteHeader(http.StatusNoContent)
}
