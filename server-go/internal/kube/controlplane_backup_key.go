package kube

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// ControlPlaneBackupKeySecret holds the passphrase the kuso-postgres-backup
// CronJob encrypts the control-plane dump with. It carries the backup-key
// role label, so `kuso backup keys export` escrows it with the addon keys.
const ControlPlaneBackupKeySecret = "kuso-controlplane-backup-key"

// EnsureControlPlaneBackupKey creates the key Secret when it is missing and
// never touches an existing one: dumps already in the bucket need the key
// they were sealed with. Safe to race across replicas (AlreadyExists wins).
func EnsureControlPlaneBackupKey(ctx context.Context, cs kubernetes.Interface, ns string) (created bool, err error) {
	if _, err := cs.CoreV1().Secrets(ns).Get(ctx, ControlPlaneBackupKeySecret, metav1.GetOptions{}); err == nil {
		return false, nil
	} else if !apierrors.IsNotFound(err) {
		return false, fmt.Errorf("read %s: %w", ControlPlaneBackupKeySecret, err)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return false, fmt.Errorf("generate control-plane backup key: %w", err)
	}
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ControlPlaneBackupKeySecret,
			Namespace: ns,
			Labels: map[string]string{
				"kuso.sislelabs.com/role":   "backup-key",
				"app.kubernetes.io/name":    "kuso-postgres-backup",
				"app.kubernetes.io/part-of": "kuso",
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{"key": []byte(hex.EncodeToString(raw))},
	}
	if _, err := cs.CoreV1().Secrets(ns).Create(ctx, sec, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return false, nil
		}
		return false, fmt.Errorf("create %s: %w", ControlPlaneBackupKeySecret, err)
	}
	return true, nil
}

// ControlPlaneBackupKey returns the passphrase, "" when the Secret is absent.
func ControlPlaneBackupKey(ctx context.Context, cs kubernetes.Interface, ns string) (string, error) {
	sec, err := cs.CoreV1().Secrets(ns).Get(ctx, ControlPlaneBackupKeySecret, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %w", ControlPlaneBackupKeySecret, err)
	}
	return string(sec.Data["key"]), nil
}
