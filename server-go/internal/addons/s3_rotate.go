package addons

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/kube"
)

// S3 credential rotation (BLOB-2). The kusoaddon chart never rotates a
// key on its own: it reads every credential back from its Secret. This
// is the explicit path, and the only way an addon created before scoped
// credentials moves its apps off the MinIO root key.
//
// Rotation is two-phase so no running app loses access mid-flight:
//  1. RotateS3Credentials creates a new bucket-scoped user in MinIO (a
//     Job using the root Secret), and only once it exists writes the new
//     key into <addon>-conn, remembering the old key in an annotation.
//     Apps pick the new key up on their next restart.
//  2. RevokePreviousS3Credentials, run after those restarts, removes the
//     old user. It refuses to touch the root user.
//
// MinIO root cannot be revoked through its API, only replaced: with
// RotateRoot the root Secret gets a new password and MinIO restarts to
// read it (BLOB-3), so a root key that leaked while apps held it stops
// working. Nothing else reads root except the chart's init Job, the
// backup CronJob and these admin Jobs, which all read the Secret at start.

const (
	annS3PreviousKey = "kuso.sislelabs.com/s3-previous-access-key"
	s3RootSuffix     = "-root"
	s3ReadOnlySuffix = "-readonly"
	s3RoleLabel      = "kuso.sislelabs.com/role"
	// Same image the chart's init Job uses.
	s3AdminImage = "pgsty/mc:RELEASE.2026-10-26T00-00-00Z"
	// rwPolicyJSON must match the chart's (kusoaddon/templates/s3.yaml).
	s3RWPolicyFmt = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetBucketLocation","s3:ListBucket","s3:ListBucketMultipartUploads","s3:ListBucketVersions"],"Resource":["arn:aws:s3:::%s"]},{"Effect":"Allow","Action":["s3:GetObject","s3:GetObjectVersion","s3:GetObjectTagging","s3:PutObject","s3:PutObjectTagging","s3:DeleteObject","s3:DeleteObjectVersion","s3:AbortMultipartUpload","s3:ListMultipartUploadParts"],"Resource":["arn:aws:s3:::%s/*"]}]}`
)

// s3AdminJobTimeout bounds how long the API waits for the MinIO admin Job.
var s3AdminJobTimeout = 2 * time.Minute

// s3AdminPoll is the Job status poll interval (tests shorten it).
var s3AdminPoll = 2 * time.Second

// S3RotateResult is what a rotation reports back.
type S3RotateResult struct {
	AccessKeyID         string `json:"accessKeyId"`
	PreviousAccessKeyID string `json:"previousAccessKeyId,omitempty"`
	// PreviousWasRoot: the addon predates scoped credentials; the old key
	// is MinIO root, which stays (it is not revocable) but no app holds
	// it any more once they restart.
	PreviousWasRoot bool `json:"previousWasRoot"`
	// RootRotated: MinIO now runs with a new root password; the old one
	// is dead.
	RootRotated bool   `json:"rootRotated"`
	Message     string `json:"message"`
}

// S3RotateOptions tunes a rotation.
type S3RotateOptions struct {
	// RotateRoot also replaces the MinIO root password (one restart).
	RotateRoot bool `json:"rotateRoot,omitempty"`
}

// annS3RootRotatedAt on the MinIO pod template forces the restart that
// makes MinIO read a rotated root password.
const annS3RootRotatedAt = "kuso.sislelabs.com/s3-root-rotated-at"

func randString(n int) (string, error) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		v, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		b[i] = alphabet[v.Int64()]
	}
	return string(b), nil
}

func (s *Service) ownedS3Addon(ctx context.Context, project, name string) (*kube.KusoAddon, string, error) {
	a, err := s.GetOwned(ctx, project, name)
	if err != nil {
		return nil, "", err
	}
	if a.Spec.Kind != "s3" || a.Spec.External != nil || a.Spec.UseInstanceAddon != "" {
		return nil, "", fmt.Errorf("%w: credential rotation applies to in-cluster s3 addons only", ErrInvalid)
	}
	return a, s.nsFor(ctx, project), nil
}

// RotateS3Credentials mints a new bucket-scoped app user and swaps it
// into the addon's conn Secret. See the file comment.
func (s *Service) RotateS3Credentials(ctx context.Context, project, name string, opts S3RotateOptions) (*S3RotateResult, error) {
	a, ns, err := s.ownedS3Addon(ctx, project, name)
	if err != nil {
		return nil, err
	}
	secrets := s.Kube.Clientset.CoreV1().Secrets(ns)
	root, err := secrets.Get(ctx, a.Name+s3RootSuffix, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("%w: %s has no root Secret yet; wait for the addon to reconcile on this kuso version", ErrConflict, a.Name)
	}
	if err != nil {
		return nil, fmt.Errorf("read root secret: %w", err)
	}
	conn, err := secrets.Get(ctx, connSecretName(a.Name), metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("read conn secret: %w", err)
	}
	if prev := conn.Annotations[annS3PreviousKey]; prev != "" {
		return nil, fmt.Errorf("%w: the previous key %s is not revoked yet; restart the services using %s, then revoke it first", ErrConflict, prev, conn.Name)
	}
	oldKey := string(conn.Data["S3_ACCESS_KEY_ID"])
	bucket := string(conn.Data["S3_BUCKET"])
	if oldKey == "" || bucket == "" {
		return nil, fmt.Errorf("%w: conn secret %s lacks S3_ACCESS_KEY_ID/S3_BUCKET", ErrInvalid, conn.Name)
	}
	rootUser := string(root.Data["MINIO_ROOT_USER"])
	wasRoot := oldKey == rootUser
	if wasRoot {
		// A pre-scoping MinIO reads its root from the conn Secret, which is
		// about to hold the app user. Re-point it at the root Secret first
		// (what the chart renders once the addon is no longer legacy), so a
		// MinIO restart between the swap and the next reconcile can never
		// boot with the app key as root. The root Secret was seeded from
		// these same values; refuse if they have since diverged.
		if string(root.Data["MINIO_ROOT_PASSWORD"]) != string(conn.Data["S3_SECRET_ACCESS_KEY"]) {
			return nil, fmt.Errorf("%w: %s and %s disagree on the root password; not rotating", ErrConflict, root.Name, conn.Name)
		}
		if err := s.pointMinIOAtRootSecret(ctx, ns, a.Name, root.Name); err != nil {
			return nil, err
		}
	}
	newKey, err := randString(20)
	if err != nil {
		return nil, err
	}
	newSecret, err := randString(40)
	if err != nil {
		return nil, err
	}
	script := s3RotateScript
	env := []corev1.EnvVar{
		{Name: "MINIO_HOST", Value: a.Name},
		{Name: "BUCKET", Value: bucket},
		{Name: "RW_POLICY_JSON", Value: fmt.Sprintf(s3RWPolicyFmt, bucket, bucket)},
	}
	if err := s.runS3AdminJob(ctx, ns, a, "rotate", script, root.Name, env, map[string][]byte{
		"NEW_KEY": []byte(newKey), "NEW_SECRET": []byte(newSecret),
	}); err != nil {
		return nil, err
	}

	upd := conn.DeepCopy()
	for _, k := range []string{"S3_ACCESS_KEY_ID", "AWS_ACCESS_KEY_ID"} {
		upd.Data[k] = []byte(newKey)
	}
	for _, k := range []string{"S3_SECRET_ACCESS_KEY", "AWS_SECRET_ACCESS_KEY"} {
		upd.Data[k] = []byte(newSecret)
	}
	if !wasRoot {
		if upd.Annotations == nil {
			upd.Annotations = map[string]string{}
		}
		upd.Annotations[annS3PreviousKey] = oldKey
	}
	if _, err := secrets.Update(ctx, upd, metav1.UpdateOptions{}); err != nil {
		return nil, fmt.Errorf("write rotated conn secret (the new MinIO user exists but is unused; retrying is safe): %w", err)
	}
	res := &S3RotateResult{AccessKeyID: newKey, PreviousWasRoot: wasRoot}
	if wasRoot {
		res.Message = "conn Secret now holds a bucket-scoped user instead of MinIO root. MinIO restarts once to read root from " + root.Name + ". Restart the services that use " + conn.Name + "."
	} else {
		res.PreviousAccessKeyID = oldKey
		res.Message = "restart the services that use " + conn.Name + ", then revoke the previous key"
	}
	if opts.RotateRoot {
		if err := s.rotateS3RootPassword(ctx, ns, a, root.Name); err != nil {
			return nil, fmt.Errorf("the app key was rotated (%s now holds %s) but the root password was not: %w", conn.Name, newKey, err)
		}
		res.RootRotated = true
		res.Message += " The MinIO root password was replaced and MinIO restarted: anything still using the old root key"
		if wasRoot {
			res.Message += " (on this addon, every service not yet restarted)"
		}
		res.Message += " is now refused."
	}
	return res, nil
}

// rotateS3RootPassword writes a new root password into the root Secret,
// restarts MinIO onto it, and proves the new password works. A failed
// restart patch puts the old password back so the Secret never disagrees
// with a running MinIO.
func (s *Service) rotateS3RootPassword(ctx context.Context, ns string, a *kube.KusoAddon, rootName string) error {
	secrets := s.Kube.Clientset.CoreV1().Secrets(ns)
	cur, err := secrets.Get(ctx, rootName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("read root secret: %w", err)
	}
	oldPass := append([]byte(nil), cur.Data["MINIO_ROOT_PASSWORD"]...)
	newPass, err := randString(40)
	if err != nil {
		return err
	}
	upd := cur.DeepCopy()
	upd.Data["MINIO_ROOT_PASSWORD"] = []byte(newPass)
	written, err := secrets.Update(ctx, upd, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("write new root password: %w", err)
	}
	if err := s.restartMinIOOnRootSecret(ctx, ns, a.Name, rootName); err != nil {
		back := written.DeepCopy()
		back.Data["MINIO_ROOT_PASSWORD"] = oldPass
		if _, rerr := secrets.Update(ctx, back, metav1.UpdateOptions{}); rerr != nil {
			return fmt.Errorf("%w; putting the old root password back into %s also failed (%v): restart MinIO by hand so it reads the new one", err, rootName, rerr)
		}
		return err
	}
	if err := s.runS3AdminJob(ctx, ns, a, "rootcheck", s3RootCheckScript, rootName, []corev1.EnvVar{{Name: "MINIO_HOST", Value: a.Name}}, nil); err != nil {
		return fmt.Errorf("MinIO was restarted onto the new root password in %s but has not confirmed it (it may still be starting; check its pod): %w", rootName, err)
	}
	return nil
}

// restartMinIOOnRootSecret points MinIO's root env at the root Secret and
// stamps the pod template so the single pod restarts and re-reads it.
func (s *Service) restartMinIOOnRootSecret(ctx context.Context, ns, sts, rootSecret string) error {
	patch, _ := json.Marshal(map[string]any{"spec": map[string]any{"template": map[string]any{
		"metadata": map[string]any{"annotations": map[string]string{annS3RootRotatedAt: time.Now().UTC().Format(time.RFC3339)}},
		"spec":     minIORootEnvPatch(rootSecret),
	}}})
	if _, err := s.Kube.Clientset.AppsV1().StatefulSets(ns).Patch(ctx, sts, types.StrategicMergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("restart MinIO: %w", err)
	}
	return nil
}

func minIORootEnvPatch(rootSecret string) map[string]any {
	ref := func(key string) map[string]any {
		return map[string]any{"name": key, "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": rootSecret, "key": key}}}
	}
	return map[string]any{"containers": []any{map[string]any{"name": "minio", "env": []any{ref("MINIO_ROOT_USER"), ref("MINIO_ROOT_PASSWORD")}}}}
}

// RevokePreviousS3Credentials removes the app user a rotation replaced.
func (s *Service) RevokePreviousS3Credentials(ctx context.Context, project, name string) (string, error) {
	a, ns, err := s.ownedS3Addon(ctx, project, name)
	if err != nil {
		return "", err
	}
	secrets := s.Kube.Clientset.CoreV1().Secrets(ns)
	conn, err := secrets.Get(ctx, connSecretName(a.Name), metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("read conn secret: %w", err)
	}
	prev := conn.Annotations[annS3PreviousKey]
	if prev == "" {
		return "", fmt.Errorf("%w: no previous key to revoke", ErrNotFound)
	}
	root, err := secrets.Get(ctx, a.Name+s3RootSuffix, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("read root secret: %w", err)
	}
	if prev == string(root.Data["MINIO_ROOT_USER"]) || prev == string(conn.Data["S3_ACCESS_KEY_ID"]) {
		return "", fmt.Errorf("%w: refusing to remove the root or current key", ErrInvalid)
	}
	script := `set -eu
i=0; until mc alias set local "http://${MINIO_HOST}:9000" "${ROOT_USER}" "${ROOT_PASSWORD}" >/dev/null 2>&1; do i=$((i+1)); [ "$i" -ge 30 ] && { echo "MinIO unreachable" >&2; exit 1; }; sleep 2; done
if mc admin user info local "${OLD_KEY}" >/dev/null 2>&1; then
  mc admin user remove local "${OLD_KEY}"
fi
echo "==> previous app user removed"
`
	if err := s.runS3AdminJob(ctx, ns, a, "revoke", script, root.Name, []corev1.EnvVar{{Name: "MINIO_HOST", Value: a.Name}},
		map[string][]byte{"OLD_KEY": []byte(prev)}); err != nil {
		return "", err
	}
	upd := conn.DeepCopy()
	delete(upd.Annotations, annS3PreviousKey)
	if _, err := secrets.Update(ctx, upd, metav1.UpdateOptions{}); err != nil {
		return "", fmt.Errorf("clear previous-key annotation: %w", err)
	}
	return prev, nil
}

// pointMinIOAtRootSecret patches the MinIO StatefulSet's root env to
// read from the root Secret (a rolling restart of the single pod).
func (s *Service) pointMinIOAtRootSecret(ctx context.Context, ns, sts, rootSecret string) error {
	patch, _ := json.Marshal(map[string]any{"spec": map[string]any{"template": map[string]any{"spec": minIORootEnvPatch(rootSecret)}}})
	if _, err := s.Kube.Clientset.AppsV1().StatefulSets(ns).Patch(ctx, sts, types.StrategicMergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("point MinIO at %s: %w", rootSecret, err)
	}
	return nil
}

// runS3AdminJob runs script with mc against the addon's MinIO as root and
// waits for it. Credentials travel in a Secret the Job owns, never in the
// Job spec.
func (s *Service) runS3AdminJob(ctx context.Context, ns string, a *kube.KusoAddon, verb, script, rootSecret string, env []corev1.EnvVar, creds map[string][]byte) error {
	name := fmt.Sprintf("%s-s3-%s-%s", a.Name, verb, strconv.FormatInt(time.Now().Unix(), 36))
	if len(name) > 63 {
		name = name[len(name)-63:]
	}
	labels := map[string]string{
		kube.LabelProject:              a.Spec.Project,
		"kuso.sislelabs.com/addon":     a.Name,
		"kuso.sislelabs.com/role":      "s3-admin",
		"app.kubernetes.io/managed-by": "kuso",
	}
	secrets := s.Kube.Clientset.CoreV1().Secrets(ns)
	if _, err := secrets.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
		Data:       creds,
	}, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create admin job secret: %w", err)
	}
	secretRef := func(n, sec, key string) corev1.EnvVar {
		return corev1.EnvVar{Name: n, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: sec}, Key: key}}}
	}
	env = append(env,
		secretRef("ROOT_USER", rootSecret, "MINIO_ROOT_USER"),
		secretRef("ROOT_PASSWORD", rootSecret, "MINIO_ROOT_PASSWORD"),
		corev1.EnvVar{Name: "HOME", Value: "/tmp"},
	)
	for k := range creds {
		env = append(env, secretRef(k, name, k))
	}
	uid := int64(1001)
	ttl := int32(600)
	backoff := int32(2)
	f := false
	t := true
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: &t, RunAsUser: &uid, RunAsGroup: &uid, FSGroup: &uid,
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{{
						Name:    "mc",
						Image:   s3AdminImage,
						Command: []string{"sh", "-c", script},
						Env:     env,
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: &f,
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
						VolumeMounts: []corev1.VolumeMount{{Name: "home", MountPath: "/tmp"}},
					}},
					Volumes: []corev1.Volume{{Name: "home", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
				},
			},
		},
	}
	created, err := s.Kube.Clientset.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		_ = secrets.Delete(ctx, name, metav1.DeleteOptions{})
		return fmt.Errorf("create admin job: %w", err)
	}
	// The creds Secret goes with the Job (TTL reaps both).
	owner := metav1.OwnerReference{APIVersion: "batch/v1", Kind: "Job", Name: created.Name, UID: created.UID}
	if sec, gerr := secrets.Get(ctx, name, metav1.GetOptions{}); gerr == nil {
		sec.OwnerReferences = []metav1.OwnerReference{owner}
		_, _ = secrets.Update(ctx, sec, metav1.UpdateOptions{})
	}

	deadline := time.Now().Add(s3AdminJobTimeout)
	for {
		j, err := s.Kube.Clientset.BatchV1().Jobs(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("watch admin job %s: %w", name, err)
		}
		for _, c := range j.Status.Conditions {
			if c.Status != corev1.ConditionTrue {
				continue
			}
			switch c.Type {
			case batchv1.JobComplete:
				return nil
			case batchv1.JobFailed:
				return fmt.Errorf("MinIO admin job %s failed (see its pod logs); nothing was changed in %s", name, connSecretName(a.Name))
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("MinIO admin job %s did not finish in %s; nothing was changed in %s", name, s3AdminJobTimeout, connSecretName(a.Name))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s3AdminPoll):
		}
	}
}

// s3RootCheckScript waits out the MinIO restart and proves the root
// Secret's password is the one MinIO now runs with.
const s3RootCheckScript = `set -eu
i=0; until mc alias set local "http://${MINIO_HOST}:9000" "${ROOT_USER}" "${ROOT_PASSWORD}" >/dev/null 2>&1; do i=$((i+1)); [ "$i" -ge 50 ] && { echo "MinIO does not accept the new root password" >&2; exit 1; }; sleep 2; done
mc admin info local >/dev/null
echo "==> MinIO runs with the new root password"
`

// s3RotateScript runs in s3AdminImage, which ships no grep/awk/sed.
const s3RotateScript = `set -eu
i=0; until mc alias set local "http://${MINIO_HOST}:9000" "${ROOT_USER}" "${ROOT_PASSWORD}" >/dev/null 2>&1; do i=$((i+1)); [ "$i" -ge 30 ] && { echo "MinIO unreachable" >&2; exit 1; }; sleep 2; done
printf '%s\n' "${RW_POLICY_JSON}" > /tmp/rw.json
mc admin policy create local "kuso-rw-${BUCKET}" /tmp/rw.json >/dev/null
mc admin user add local "${NEW_KEY}" "${NEW_SECRET}" >/dev/null
mc admin policy attach local "kuso-rw-${BUCKET}" --user "${NEW_KEY}" >/dev/null 2>&1 || true
# pgsty/mc ships no grep/awk/sed: match with the shell.
info=$(mc admin user info local "${NEW_KEY}")
case "$info" in *"kuso-rw-${BUCKET}"*) ;; *) echo "policy kuso-rw-${BUCKET} not attached to the new user" >&2; exit 1 ;; esac
echo "==> new app user ready"
`
