package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/addons"
	"kuso/server/internal/backup"
	"kuso/server/internal/kube"
)

// Backup objects are keyed by name (<project>/<addon>/), and outlive the
// addon. A same-name project or addon created later used to inherit the
// predecessor's dumps: list, download and restore them, and prune them
// (BLOB-3/4). Objects are never moved or rewritten; instead ownership is
// decided per object:
//
//   - an object stamped at or after the live CR's creationTimestamp was
//     written by that CR's CronJob;
//   - an older object belongs to the live CR only when the prefix's owner
//     marker names the CR's UID. The marker is written once, on the
//     first boot after upgrade, for every addon that exists then
//     (ClaimLegacyBackupOwners), so legitimate owners whose CR predates
//     the marker scheme keep their history.
//
// Anything else is a predecessor's data: invisible to the new owner, kept
// in the bucket, and removable only through the audited admin purge.
const (
	backupOwnerMarker     = ".kuso-owner.json"
	backupOwnerClaimsDone = "kuso-meta/backup-owner-claims-v1"
	// backupOwnerSkew tolerates the apiserver clock running ahead of the
	// dump pod's.
	backupOwnerSkew = 2 * time.Minute
)

type backupOwnerRecord struct {
	AddonUID  string    `json:"addonUID"`
	Project   string    `json:"project"`
	Addon     string    `json:"addon"`
	ClaimedAt time.Time `json:"claimedAt"`
}

var backupStampRe = regexp.MustCompile(`^(\d{8}T\d{6}Z)`)

// backupObjectTime is when a backup object was taken: the stamp every
// backup key starts with, or the object's LastModified for a key without
// one.
func backupObjectTime(prefix, key string, lastModified time.Time) time.Time {
	rest := strings.TrimPrefix(key, prefix)
	if m := backupStampRe.FindStringSubmatch(rest); m != nil {
		if t, err := time.Parse("20060102T150405Z", m[1]); err == nil {
			return t
		}
	}
	return lastModified
}

// backupOwnership is the per-object ownership rule for one live CR.
type backupOwnership struct {
	since   time.Time
	claimed bool
}

func ownershipFor(created time.Time, uid string, marker *backupOwnerRecord) backupOwnership {
	o := backupOwnership{since: created.Add(-backupOwnerSkew)}
	if marker != nil && uid != "" && marker.AddonUID == uid {
		o.claimed = true
	}
	return o
}

func (o backupOwnership) owns(taken time.Time) bool {
	return o.claimed || !taken.Before(o.since)
}

type s3ObjectReader interface {
	GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

func isS3NotFound(err error) bool {
	var nsk *s3types.NoSuchKey
	var nf *s3types.NotFound
	var re *awshttp.ResponseError
	return errors.As(err, &nsk) || errors.As(err, &nf) || (errors.As(err, &re) && re.HTTPStatusCode() == http.StatusNotFound)
}

// readBackupOwner returns the prefix's owner marker, nil when there is none.
func readBackupOwner(ctx context.Context, cli s3ObjectReader, bucket, prefix string) (*backupOwnerRecord, error) {
	out, err := cli.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(prefix + backupOwnerMarker)})
	if err != nil {
		if isS3NotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	defer out.Body.Close()
	var rec backupOwnerRecord
	if err := json.NewDecoder(io.LimitReader(out.Body, 64*1024)).Decode(&rec); err != nil {
		return nil, fmt.Errorf("parse owner marker: %w", err)
	}
	return &rec, nil
}

// addonBackupOwnership resolves the ownership rule for cr's prefix. A
// marker that can't be read is treated as absent: that only hides
// pre-creation objects, never shows a predecessor's.
func (h *BackupsHandler) addonBackupOwnership(ctx context.Context, cli s3ObjectReader, bucket, prefix string, cr *kube.KusoAddon) backupOwnership {
	marker, err := readBackupOwner(ctx, cli, bucket, prefix)
	if err != nil {
		h.Logger.Warn("backup: read owner marker; showing only objects newer than the addon", "prefix", prefix, "err", err)
	}
	return ownershipFor(cr.CreationTimestamp.Time, string(cr.UID), marker)
}

func isOwnerMarkerKey(key string) bool {
	return key == backupOwnerMarker || strings.HasSuffix(key, "/"+backupOwnerMarker)
}

// backupClaimClient is the S3 surface ClaimLegacyBackupOwners needs.
type backupClaimClient interface {
	s3ObjectReader
	s3.ListObjectsV2APIClient
	PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	HeadObject(ctx context.Context, in *s3.HeadObjectInput, opts ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
}

// ClaimLegacyBackupOwners writes the owner marker for every live addon
// whose prefix already holds backups, once per bucket. It runs at boot;
// after the first successful pass a done-marker makes it a no-op, so an
// addon created later never claims a predecessor's dumps. Nothing is
// moved, renamed or deleted.
func (h *BackupsHandler) ClaimLegacyBackupOwners(ctx context.Context) error {
	cli, bucket, err := h.s3Client(ctx)
	if err != nil {
		return nil // backups not configured: there is nothing to claim
	}
	raw, err := h.Kube.Dynamic.Resource(kube.GVRAddons).Namespace(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list addons: %w", err)
	}
	var crs []kube.KusoAddon
	for i := range raw.Items {
		var a kube.KusoAddon
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw.Items[i].Object, &a); err != nil {
			return fmt.Errorf("decode addon %s: %w", raw.Items[i].GetName(), err)
		}
		crs = append(crs, a)
	}
	return claimLegacyBackupOwners(ctx, cli, bucket, crs, time.Now())
}

func claimLegacyBackupOwners(ctx context.Context, cli backupClaimClient, defaultBucket string, crs []kube.KusoAddon, now time.Time) error {
	if _, err := cli.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(defaultBucket), Key: aws.String(backupOwnerClaimsDone)}); err == nil {
		return nil
	} else if !isS3NotFound(err) {
		return fmt.Errorf("check claim marker: %w", err)
	}
	for i := range crs {
		cr := &crs[i]
		if cr.Spec.UseInstanceAddon != "" || cr.UID == "" {
			continue // tenants have no prefix of their own
		}
		project := cr.Spec.Project
		if project == "" {
			project = cr.Labels[kube.LabelProject]
		}
		if project == "" {
			continue
		}
		bucket := cr.BackupBucket(defaultBucket)
		prefix := project + "/" + cr.Name + "/"
		page, err := cli.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket), Prefix: aws.String(prefix), MaxKeys: aws.Int32(1)})
		if err != nil {
			return fmt.Errorf("list %s: %w", prefix, err)
		}
		if len(page.Contents) == 0 {
			continue
		}
		existing, err := readBackupOwner(ctx, cli, bucket, prefix)
		if err != nil {
			return fmt.Errorf("read marker %s: %w", prefix, err)
		}
		if existing != nil {
			continue
		}
		body, _ := json.Marshal(backupOwnerRecord{AddonUID: string(cr.UID), Project: project, Addon: cr.Name, ClaimedAt: now.UTC()})
		if _, err := cli.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket), Key: aws.String(prefix + backupOwnerMarker),
			Body: strings.NewReader(string(body)), ContentType: aws.String("application/json"),
		}); err != nil {
			return fmt.Errorf("write marker %s: %w", prefix, err)
		}
	}
	_, err := cli.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(defaultBucket), Key: aws.String(backupOwnerClaimsDone),
		Body: strings.NewReader(now.UTC().Format(time.RFC3339)),
	})
	if err != nil {
		return fmt.Errorf("write claim marker: %w", err)
	}
	return nil
}

// readBackupKey returns the encryption key, "" when the Secret doesn't
// exist (backups taken before encryption shipped).
func (h *BackupsHandler) readBackupKey(ctx context.Context, ns, name string) (string, error) {
	sec, err := h.Kube.Clientset.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read backup key %s/%s: %w", ns, name, err)
	}
	return string(sec.Data[backup.KeySecretKey]), nil
}

// restoreKeyEnv returns the BACKUP_ENCRYPTION_KEY env for a restore Job
// of key. The key Secret normally sits beside the Job; an instance
// tenant's dumps were encrypted with the instance addon's key, which
// lives in the home namespace, so that key is copied into a Secret the
// Job owns (keyCopy=true; the caller adopts or deletes it). Optional
// either way: a dump taken before encryption shipped needs no key.
func (h *BackupsHandler) restoreKeyEnv(ctx context.Context, ns, jobName, project, key string, tenantLoc *instanceTenantBackupLoc) (corev1.EnvVar, bool, error) {
	if tenantLoc == nil {
		seg, _, _ := strings.Cut(strings.TrimPrefix(key, project+"/"), "/")
		return envFromSecretOptional("BACKUP_ENCRYPTION_KEY", backup.KeySecretName(seg), backup.KeySecretKey), false, nil
	}
	if tenantLoc.keyNS == ns {
		return envFromSecretOptional("BACKUP_ENCRYPTION_KEY", tenantLoc.keyName, backup.KeySecretKey), false, nil
	}
	val, err := h.readBackupKey(ctx, tenantLoc.keyNS, tenantLoc.keyName)
	if err != nil {
		return corev1.EnvVar{}, false, err
	}
	copyName := jobName + "-key"
	if val == "" {
		return envFromSecretOptional("BACKUP_ENCRYPTION_KEY", copyName, backup.KeySecretKey), false, nil
	}
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: copyName, Namespace: ns, Labels: map[string]string{kube.LabelProject: project}},
		Data:       map[string][]byte{backup.KeySecretKey: []byte(val)},
	}
	if _, err := h.Kube.Clientset.CoreV1().Secrets(ns).Create(ctx, sec, metav1.CreateOptions{}); err != nil {
		return corev1.EnvVar{}, false, fmt.Errorf("copy backup key: %w", err)
	}
	return envFromSecretOptional("BACKUP_ENCRYPTION_KEY", copyName, backup.KeySecretKey), true, nil
}

// adoptRestoreKeyCopy makes the copied key Secret cascade-delete with
// the restore Job (whose TTL reaps it).
func (h *BackupsHandler) adoptRestoreKeyCopy(ctx context.Context, ns string, job *batchv1.Job) {
	patch := fmt.Sprintf(`{"metadata":{"ownerReferences":[{"apiVersion":"batch/v1","kind":"Job","name":%q,"uid":%q}]}}`, job.Name, job.UID)
	if _, err := h.Kube.Clientset.CoreV1().Secrets(ns).Patch(ctx, job.Name+"-key", types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		h.Logger.Warn("backup: adopt restore key copy", "job", job.Name, "err", err)
	}
}

type s3ObjectHeader interface {
	HeadObject(ctx context.Context, in *s3.HeadObjectInput, opts ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
}

// backupKeyTime is backupObjectTime for a single key, asking S3 for
// LastModified only when the key carries no stamp.
func backupKeyTime(ctx context.Context, cli s3ObjectHeader, bucket, prefix, key string) (time.Time, error) {
	if t := backupObjectTime(prefix, key, time.Time{}); !t.IsZero() {
		return t, nil
	}
	out, err := cli.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return time.Time{}, err
	}
	return aws.ToTime(out.LastModified), nil
}

// restoreKeyOwned returns (0, "") when key is a dump the caller's project
// currently owns, else the HTTP status and message to refuse with.
func (h *BackupsHandler) restoreKeyOwned(ctx context.Context, project, key string, tenantLoc *instanceTenantBackupLoc) (int, string) {
	cli, defBucket, err := h.s3Client(ctx)
	if err != nil {
		return http.StatusServiceUnavailable, err.Error()
	}
	if tenantLoc != nil {
		bucket := defBucket
		if tenantLoc.bucket != "" {
			bucket = tenantLoc.bucket
		}
		t, err := backupKeyTime(ctx, cli, bucket, tenantLoc.prefix, key)
		if err != nil {
			return http.StatusBadGateway, "stat backup: " + err.Error()
		}
		if !tenantLoc.ownsAt(key, t) {
			return http.StatusBadRequest, "backup belongs to a previous database of the same name"
		}
		return 0, ""
	}
	rest := strings.TrimPrefix(key, project+"/")
	seg, _, ok := strings.Cut(rest, "/")
	if !ok || seg == "" {
		return http.StatusBadRequest, "key must live under an addon's prefix"
	}
	owner, _, err := h.ownedAddon(ctx, project, seg)
	if err != nil {
		if errors.Is(err, addons.ErrNotFound) {
			return http.StatusBadRequest, "backup belongs to an addon that no longer exists"
		}
		return http.StatusBadGateway, err.Error()
	}
	prefix := project + "/" + owner.Name + "/"
	if !strings.HasPrefix(key, prefix) {
		return http.StatusBadRequest, "key must live under the addon's own prefix"
	}
	bucket := owner.BackupBucket(defBucket)
	t, err := backupKeyTime(ctx, cli, bucket, prefix, key)
	if err != nil {
		return http.StatusBadGateway, "stat backup: " + err.Error()
	}
	if !h.addonBackupOwnership(ctx, cli, bucket, prefix, owner).owns(t) {
		return http.StatusBadRequest, "backup belongs to a previous addon of the same name"
	}
	return 0, ""
}
