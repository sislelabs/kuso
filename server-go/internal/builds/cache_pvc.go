// Helpers that materialise the per-build Kubernetes objects the
// kaniko pod consumes: a short-lived clone-token Secret and a
// per-service build-cache PVC. Extracted from builds.go in the v0.12
// refactor pass — these are infra side-effects that don't belong with
// the lifecycle / admission paths.
package builds

import (
	"context"
	"fmt"
	"log/slog"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// ensureCloneTokenSecret upserts the <buildName>-token Secret used by
// the clone init container. We mint a fresh installation token when
// the github client is wired AND an installation id is set; otherwise
// we still write a secret (with empty token) so pods can start and
// surface a clean clone error instead of wedging on
// CreateContainerConfigError.
func (s *Service) ensureCloneTokenSecret(ctx context.Context, ns, buildName string, installationID int64) error {
	token := ""
	if s.Tokens != nil && installationID > 0 {
		t, err := s.Tokens.MintInstallationToken(ctx, installationID)
		if err != nil {
			return fmt.Errorf("mint installation token: %w", err)
		}
		token = t
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      buildName + "-token",
			Namespace: ns,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "kuso-server",
				"kuso.sislelabs.com/build":     buildName,
			},
		},
		Type:       corev1.SecretTypeOpaque,
		StringData: map[string]string{"token": token},
	}
	_, err := s.Kube.Clientset.CoreV1().Secrets(ns).Create(ctx, secret, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		// Refresh in place — token is short-lived, so reusing a stale
		// one risks a clone failure on retry.
		_, uerr := s.Kube.Clientset.CoreV1().Secrets(ns).Update(ctx, secret, metav1.UpdateOptions{})
		if apierrors.IsNotFound(uerr) {
			// Concurrent cancel/cleanup deleted the Secret between our
			// Create-returns-409 and the Update. Retry Create — same
			// caller intent (upsert), so we transparently fall back
			// rather than surface a confusing 404 from an Update path.
			_, uerr = s.Kube.Clientset.CoreV1().Secrets(ns).Create(ctx, secret, metav1.CreateOptions{})
		}
		if uerr != nil {
			return fmt.Errorf("upsert clone token secret %s/%s: %w", ns, secret.Name, uerr)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("create clone token secret %s/%s: %w", ns, secret.Name, err)
	}
	return nil
}

// ensureBuildCachePVC upserts a PVC named <fqn>-build-cache in the
// project's namespace. Each build mounts it at /cache and uses
// /cache/nix as the persistent nix store + /cache/deps as the
// per-language dep cache root. Owned by the KusoService so it
// cascade-deletes when the service is deleted.
//
// Idempotent: returns nil if the PVC already exists. Size is fixed
// at first creation; resizing later requires either kube's volume-
// resize feature flag (which most installs don't have) or manual PV
// recreation.
//
// Best-effort: on any kube error we return "" and log a warn —
// the build still runs without the cache (just slower). The cache
// is a perf optimisation, not a correctness requirement.
func (s *Service) ensureBuildCachePVC(ctx context.Context, ns, fqn string, svcCR *kube.KusoService, sizeGi int) string {
	if s.Kube == nil {
		return ""
	}
	pvcName := fqn + "-build-cache"
	if sizeGi <= 0 {
		sizeGi = 5
	}
	// OwnerReference back to the KusoService — cascade-delete when
	// the user removes the service.
	owners := []metav1.OwnerReference{}
	if svcCR != nil && svcCR.UID != "" {
		owners = append(owners, metav1.OwnerReference{
			APIVersion: "application.kuso.sislelabs.com/v1alpha1",
			Kind:       "KusoService",
			Name:       svcCR.Name,
			UID:        svcCR.UID,
		})
	}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:            pvcName,
			Namespace:       ns,
			OwnerReferences: owners,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "kuso-server",
				"kuso.sislelabs.com/service":   fqn,
				"kuso.sislelabs.com/role":      "build-cache",
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(fmt.Sprintf("%dGi", sizeGi)),
				},
			},
		},
	}
	_, err := s.Kube.Clientset.CoreV1().PersistentVolumeClaims(ns).Create(ctx, pvc, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return pvcName
	}
	if err != nil {
		slog.Default().Warn("ensureBuildCachePVC", "ns", ns, "name", pvcName, "err", err)
		return ""
	}
	return pvcName
}
