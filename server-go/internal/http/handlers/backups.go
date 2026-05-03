package handlers

import (
	"context"
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
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// BackupsHandler exposes /api/admin/backup-settings (CRUD on the
// kuso-backup-s3 Secret) and /api/projects/{p}/addons/{a}/backups
// (list + restore, gated on the same Secret existing).
type BackupsHandler struct {
	Kube      *kube.Client
	Namespace string
	Logger    *slog.Logger
}

const backupSecretName = "kuso-backup-s3"

func (h *BackupsHandler) Mount(r chi.Router) {
	r.Get("/api/admin/backup-settings", h.GetSettings)
	r.Put("/api/admin/backup-settings", h.PutSettings)
	r.Get("/api/projects/{project}/addons/{addon}/backups", h.List)
	r.Post("/api/projects/{project}/addons/{addon}/backups/restore", h.Restore)
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

	cli, bucket, err := h.s3Client(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	prefix := project + "/" + addon + "/"
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

	releaseName := project + "-" + addon
	jobName := fmt.Sprintf("%s-restore-%d", releaseName, time.Now().Unix())

	one := int32(1)
	zero := int32(0)
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: h.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "kuso-server",
				"kuso.sislelabs.com/role":      "restore",
				"kuso.sislelabs.com/project":   project,
				"kuso.sislelabs.com/addon":     addon,
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
							{Name: "POSTGRES_HOST", Value: releaseName + "-postgresql"},
							{Name: "POSTGRES_USER", Value: "kuso"},
							{Name: "POSTGRES_DB", Value: "kuso"},
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
