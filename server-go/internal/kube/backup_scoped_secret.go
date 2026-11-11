package kube

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Per-project backup credentials (BLOB-4).
//
// kuso-backup-s3 holds ONE credential with read/write/delete on the whole
// backup bucket, and every backup pod used to receive it. An admin can
// give a project its own key, limited (by a bucket policy the admin
// creates on the S3 side) to the project's <project>/ prefix. kuso keeps
// those keys in BackupScopedSecretName in the home namespace and renders,
// per project, a <BackupSecretName>-<project> Secret in the project's
// execution namespace: the instance bucket/endpoint/region with the
// project's key. The kusoaddon chart mounts that Secret instead of the
// shared one whenever it exists.
//
// Unsetting a project's key rewrites its Secret with the shared
// credential rather than deleting it: CronJobs rendered while it existed
// keep referencing it until their next reconcile, and a missing Secret
// would make those runs upload nothing.

// BackupScopedSecretName holds the per-project keys, as
// "<project>.accessKeyId" / "<project>.secretAccessKey". Never mirrored.
const BackupScopedSecretName = "kuso-backup-scoped-keys"

// LabelBackupCredential marks a rendered per-project backup Secret.
const LabelBackupCredential = "kuso.sislelabs.com/backup-credential"

// ErrBackupNotConfigured: the instance backup Secret does not exist.
var ErrBackupNotConfigured = errors.New("kube: backup storage not configured")

// ProjectBackupSecretName is the Secret the chart prefers for project.
// Mirrored by kusoaddon.backupS3SecretName.
func ProjectBackupSecretName(project string) string {
	return backupSecretName + "-" + project
}

// ScopedBackupCredential is one project's key (secret omitted on reads).
type ScopedBackupCredential struct {
	Project     string `json:"project"`
	AccessKeyID string `json:"accessKeyId"`
}

func scopedKeys(project string) (string, string) {
	return project + ".accessKeyId", project + ".secretAccessKey"
}

// ListScopedBackupCredentials returns the projects that have their own key.
func (c *Client) ListScopedBackupCredentials(ctx context.Context) ([]ScopedBackupCredential, error) {
	sec, err := c.Clientset.CoreV1().Secrets(ServerSANamespace).Get(ctx, BackupScopedSecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("kube: read %s: %w", BackupScopedSecretName, err)
	}
	var out []ScopedBackupCredential
	for k, v := range sec.Data {
		if p, ok := strings.CutSuffix(k, ".accessKeyId"); ok && p != "" {
			out = append(out, ScopedBackupCredential{Project: p, AccessKeyID: string(v)})
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Project < out[b].Project })
	return out, nil
}

// SetScopedBackupCredential stores (or, with an empty accessKeyID, drops)
// project's key and re-renders its Secret.
func (c *Client) SetScopedBackupCredential(ctx context.Context, project, accessKeyID, secretAccessKey string) error {
	secrets := c.Clientset.CoreV1().Secrets(ServerSANamespace)
	ak, sk := scopedKeys(project)
	sec, err := secrets.Get(ctx, BackupScopedSecretName, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		if accessKeyID == "" {
			return nil
		}
		sec = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: BackupScopedSecretName, Namespace: ServerSANamespace,
				Labels: map[string]string{ManagedByLabel: "kuso-server"}},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{ak: []byte(accessKeyID), sk: []byte(secretAccessKey)},
		}
		if _, err := secrets.Create(ctx, sec, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("kube: create %s: %w", BackupScopedSecretName, err)
		}
	case err != nil:
		return fmt.Errorf("kube: read %s: %w", BackupScopedSecretName, err)
	default:
		if sec.Data == nil {
			sec.Data = map[string][]byte{}
		}
		if accessKeyID == "" {
			delete(sec.Data, ak)
			delete(sec.Data, sk)
		} else {
			sec.Data[ak], sec.Data[sk] = []byte(accessKeyID), []byte(secretAccessKey)
		}
		if _, err := secrets.Update(ctx, sec, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("kube: update %s: %w", BackupScopedSecretName, err)
		}
	}
	return c.renderProjectBackupSecret(ctx, project, accessKeyID != "")
}

// HealScopedBackupSecrets re-renders every project's backup Secret from
// the current instance settings and key store: an instance endpoint or
// bucket change must reach them like it reaches the shared copies.
// Projects that never had a key (no rendered Secret) are left alone.
func (c *Client) HealScopedBackupSecrets(ctx context.Context) (int, error) {
	if c == nil || c.Clientset == nil {
		return 0, nil
	}
	// No key store: no project was ever scoped, so nothing was rendered.
	if _, err := c.Clientset.CoreV1().Secrets(ServerSANamespace).Get(ctx, BackupScopedSecretName, metav1.GetOptions{}); apierrors.IsNotFound(err) {
		return 0, nil
	}
	scoped, err := c.ListScopedBackupCredentials(ctx)
	if err != nil {
		return 0, err
	}
	projects := map[string]bool{}
	for _, s := range scoped {
		projects[s.Project] = true
	}
	// A project whose key was unset still has a rendered Secret to keep
	// current. Per-project GETs, not a cluster-wide Secret list.
	all, err := c.ListKusoProjects(ctx, ServerSANamespace)
	if err != nil {
		return 0, fmt.Errorf("kube: list projects: %w", err)
	}
	for i := range all {
		p := all[i].Name
		if _, ok := projects[p]; ok {
			continue
		}
		ns := all[i].Spec.Namespace
		if ns == "" {
			ns = ServerSANamespace
		}
		if _, gerr := c.Clientset.CoreV1().Secrets(ns).Get(ctx, ProjectBackupSecretName(p), metav1.GetOptions{}); gerr == nil {
			projects[p] = false
		}
	}
	var n int
	var firstErr error
	for p := range projects {
		if err := c.renderProjectBackupSecret(ctx, p, projects[p]); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		n++
	}
	return n, firstErr
}

// renderProjectBackupSecret writes <BackupSecretName>-<project> into the
// project's execution namespace: instance settings plus the project's key
// when scoped, the shared key otherwise.
func (c *Client) renderProjectBackupSecret(ctx context.Context, project string, scoped bool) error {
	src, err := c.Clientset.CoreV1().Secrets(ServerSANamespace).Get(ctx, backupSecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return ErrBackupNotConfigured
	}
	if err != nil {
		return fmt.Errorf("kube: read %s: %w", backupSecretName, err)
	}
	data := make(map[string][]byte, len(src.Data))
	for k, v := range src.Data {
		data[k] = v
	}
	if scoped {
		store, err := c.Clientset.CoreV1().Secrets(ServerSANamespace).Get(ctx, BackupScopedSecretName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("kube: read %s: %w", BackupScopedSecretName, err)
		}
		ak, sk := scopedKeys(project)
		if len(store.Data[ak]) == 0 || len(store.Data[sk]) == 0 {
			return fmt.Errorf("kube: %s has no complete key for %s", BackupScopedSecretName, project)
		}
		data["accessKeyId"], data["secretAccessKey"] = store.Data[ak], store.Data[sk]
	}
	ns := ServerSANamespace
	if p, err := c.GetKusoProject(ctx, ServerSANamespace, project); err == nil && p.Spec.Namespace != "" {
		ns = p.Spec.Namespace
	} else if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("kube: resolve namespace of %s: %w", project, err)
	}
	want := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: ProjectBackupSecretName(project), Namespace: ns,
			Labels: map[string]string{ManagedByLabel: ManagedByValue, LabelBackupCredential: project, LabelProject: project},
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}
	secrets := c.Clientset.CoreV1().Secrets(ns)
	_, err = secrets.Create(ctx, want, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		_, err = secrets.Update(ctx, want, metav1.UpdateOptions{})
	}
	if err != nil {
		return fmt.Errorf("kube: write %s/%s: %w", ns, want.Name, err)
	}
	return nil
}
