package kube

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// backupSecretName is the instance-wide S3 backup credential. The addon
// backup CronJob mounts it with optional:true, so when it is absent the
// job's BUCKET is empty and it exits 0 -- Kubernetes marks the Job
// Completed and kuso stamps lastSuccessAt. A missing Secret therefore
// does not look like a failure; it looks like a successful backup that
// wrote nothing.
const backupSecretName = "kuso-backup-s3"

// ensureBackupSecret mirrors the instance-wide backup credential from
// the home namespace into ns.
//
// Backup settings are stored once, in `kuso`, but every project that
// lives in its own namespace runs its CronJob there and reads the Secret
// locally. Nothing copied it across, so on this cluster five addons in
// three custom namespaces silently backed up nothing for weeks while
// reporting healthy. Propagating at EnsureNamespace time closes the gap
// for new namespaces; the boot sweep handles the ones already broken.
//
// Errors are returned so a caller that cares can log them, but callers
// must treat this as best-effort: a project namespace that exists
// without a backup Secret is degraded, not broken, and failing project
// creation over it would be worse than the gap it closes.
func (c *Client) ensureBackupSecret(ctx context.Context, ns string) error {
	if c == nil || c.Clientset == nil || ns == "" || ns == ServerSANamespace {
		return nil
	}
	src, err := c.Clientset.CoreV1().Secrets(ServerSANamespace).Get(ctx, backupSecretName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Backups genuinely aren't configured. Creating an empty
			// Secret here would be worse than none: BUCKET would still
			// be empty, but the health check's "secret missing" signal
			// -- the one thing that surfaces this -- would go quiet.
			return nil
		}
		return fmt.Errorf("kube: read %s/%s: %w", ServerSANamespace, backupSecretName, err)
	}

	want := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      backupSecretName,
			Namespace: ns,
			Labels:    map[string]string{ManagedByLabel: ManagedByValue},
		},
		Type: src.Type,
		Data: src.Data,
	}

	_, err = c.Clientset.CoreV1().Secrets(ns).Create(ctx, want, metav1.CreateOptions{})
	switch {
	case err == nil:
		return nil
	case apierrors.IsAlreadyExists(err):
		// Update rather than leave it: a rotated credential must reach
		// namespaces holding a stale copy, or those projects keep
		// authenticating with the old key and their backups start
		// failing for a reason nobody connects to the rotation.
		if _, uerr := c.Clientset.CoreV1().Secrets(ns).Update(ctx, want, metav1.UpdateOptions{}); uerr != nil {
			return fmt.Errorf("kube: refresh %s in %q: %w", backupSecretName, ns, uerr)
		}
		return nil
	default:
		return fmt.Errorf("kube: copy %s into %q: %w", backupSecretName, ns, err)
	}
}

// HealBackupSecrets copies the instance-wide backup credential into
// every kuso-managed namespace that is missing or holding a stale copy.
//
// EnsureNamespace only runs when a namespace is created or a project is
// written, so namespaces that already exist keep whatever they had --
// which on an upgrade is nothing. Without a sweep the fix would only
// help projects created after it shipped, while the ones already
// silently backing up nothing stayed broken.
//
// Returns the number of namespaces written and the first error. Safe to
// call on every boot: it is idempotent, and the per-namespace copy is a
// no-op once the payload matches.
func (c *Client) HealBackupSecrets(ctx context.Context) (int, error) {
	if c == nil || c.Clientset == nil {
		return 0, nil
	}
	// Only namespaces kuso manages. A hand-made namespace that happens
	// to hold a kuso addon is not ours to write Secrets into.
	nss, err := c.Clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{
		LabelSelector: ManagedByLabel + "=" + ManagedByValue,
	})
	if err != nil {
		return 0, fmt.Errorf("kube: list managed namespaces: %w", err)
	}
	var healed int
	var firstErr error
	for i := range nss.Items {
		ns := nss.Items[i].Name
		if ns == ServerSANamespace {
			continue
		}
		if err := c.ensureBackupSecret(ctx, ns); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		healed++
	}
	return healed, firstErr
}
