package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// helmUninstallFinalizers are the finalizer names the operator-sdk
// helm reconciler adds to every helm-managed CR. It blocks Delete
// until the helm release secret can be purged. When a CR is deleted
// before the operator ever renders the chart (or when the chart fails
// to render), the helm release secret never exists, so the finalizer
// can never be satisfied and the CR sits with deletionTimestamp set
// forever.
//
// The newer helm-operator (≥1.30) uses the operatorframework.io
// prefix; older builds used the bare name. Match either.
var helmUninstallFinalizers = []string{
	"helm.sdk.operatorframework.io/uninstall-release",
	"uninstall-helm-release",
}

// CleanupStuckHelmFinalizers strips uninstall-helm-release from any CR
// in `namespace` that has a deletionTimestamp set AND no corresponding
// `sh.helm.release.v1.<name>.*` Secret. This is the §6.5 landmine:
// without this sweep, a deleted CR with no helm release blocks all
// future operations on the same name.
//
// Returns (released, swept) — the count of CRs cleared and the count
// inspected. Errors on individual CRs are logged via an optional
// logger but don't abort the sweep.
func (c *Client) CleanupStuckHelmFinalizers(ctx context.Context, namespace string, gvr schema.GroupVersionResource, log func(msg string, kv ...any)) (released, inspected int, err error) {
	items, listErr := c.Dynamic.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if listErr != nil {
		return 0, 0, fmt.Errorf("kube: list %s for finalizer sweep: %w", gvr.Resource, listErr)
	}
	for i := range items.Items {
		obj := &items.Items[i]
		inspected++
		if obj.GetDeletionTimestamp() == nil {
			continue
		}
		fins := obj.GetFinalizers()
		if !containsAnyString(fins, helmUninstallFinalizers) {
			continue
		}
		// Helm release secrets are named `sh.helm.release.v1.<release>.v<revision>`.
		// We match prefix because we don't track revisions here.
		secrets, secErr := c.Clientset.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{
			FieldSelector: "type=helm.sh/release.v1",
		})
		if secErr != nil {
			if log != nil {
				log("finalizer sweep: list helm secrets failed", "ns", namespace, "err", secErr)
			}
			continue
		}
		releaseName := obj.GetName()
		if hasHelmReleaseSecret(secrets.Items, releaseName) {
			// Real release exists — let helm-operator do its job.
			continue
		}
		// No release secret → finalizer can never satisfy. Strip it.
		patched, patchErr := stripFinalizer(ctx, c, gvr, namespace, releaseName, fins)
		if patchErr != nil {
			if apierrors.IsNotFound(patchErr) || apierrors.IsConflict(patchErr) {
				continue
			}
			if log != nil {
				log("finalizer sweep: patch failed", "ns", namespace, "name", releaseName, "err", patchErr)
			}
			continue
		}
		if patched {
			released++
			if log != nil {
				log("finalizer sweep: cleared stuck helm finalizer", "kind", gvr.Resource, "ns", namespace, "name", releaseName)
			}
		}
	}
	return released, inspected, nil
}

// StripHelmFinalizers is the public alias of stripFinalizer used
// by the build cleanup sweep. The KusoBuild watch-selector
// (operator/watches.yaml) excludes done builds, so the operator
// never sees their delete events; without this strip, the helm-
// uninstall finalizer hangs the CR forever.
func StripHelmFinalizers(ctx context.Context, c *Client, gvr schema.GroupVersionResource, namespace, name string) error {
	obj, err := c.Dynamic.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	_, err = stripFinalizer(ctx, c, gvr, namespace, name, obj.GetFinalizers())
	return err
}

// stripFinalizer removes any helm-uninstall finalizer from finalizers
// via a merge-patch on metadata.finalizers. Returns (patched, err) —
// patched is false if no such finalizer was present after re-listing.
func stripFinalizer(ctx context.Context, c *Client, gvr schema.GroupVersionResource, namespace, name string, current []string) (bool, error) {
	drop := map[string]bool{}
	for _, f := range helmUninstallFinalizers {
		drop[f] = true
	}
	next := make([]string, 0, len(current))
	for _, f := range current {
		if drop[f] {
			continue
		}
		next = append(next, f)
	}
	if len(next) == len(current) {
		return false, nil
	}
	patch := map[string]any{"metadata": map[string]any{"finalizers": next}}
	body, err := json.Marshal(patch)
	if err != nil {
		return false, fmt.Errorf("marshal: %w", err)
	}
	_, err = c.Dynamic.Resource(gvr).Namespace(namespace).Patch(ctx, name, types.MergePatchType, body, metav1.PatchOptions{})
	if err != nil {
		return false, err
	}
	return true, nil
}

func containsAnyString(haystack []string, needles []string) bool {
	want := map[string]bool{}
	for _, n := range needles {
		want[n] = true
	}
	for _, s := range haystack {
		if want[s] {
			return true
		}
	}
	return false
}

// hasHelmReleaseSecret reports whether any helm release secret in items
// belongs to the named release. Secret names follow
// `sh.helm.release.v1.<release>.v<rev>`.
func hasHelmReleaseSecret(items []corev1.Secret, releaseName string) bool {
	prefix := "sh.helm.release.v1." + releaseName + "."
	for _, s := range items {
		if strings.HasPrefix(s.Name, prefix) {
			return true
		}
	}
	return false
}
