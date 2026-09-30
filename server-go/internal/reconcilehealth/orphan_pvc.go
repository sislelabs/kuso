package reconcilehealth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// KindOrphanAddonPVC: an addon data PVC whose KusoAddon is gone. Addon
// delete keeps the StatefulSet PVC on purpose, so every deleted addon
// leaves one behind until someone purges it — and while it exists the
// addon name can't be reused.
const KindOrphanAddonPVC Kind = "orphan_addon_pvc"

const (
	addonPVCSelector   = "app.kubernetes.io/name=kusoaddon"
	labelAddonInstance = "app.kubernetes.io/instance"
)

// detectOrphanAddonPVCs reports addon data PVCs whose owning addon CR
// (the helm release name in app.kubernetes.io/instance) is absent from
// live. Keyed on ABSENCE of the CR, never on "nothing mounts it": a
// retained PVC is unmounted by definition.
func detectOrphanAddonPVCs(pvcs []corev1.PersistentVolumeClaim, live map[string]bool) []Issue {
	var out []Issue
	for i := range pvcs {
		p := &pvcs[i]
		if p.Labels["app.kubernetes.io/name"] != "kusoaddon" || p.DeletionTimestamp != nil {
			continue
		}
		addonCR := p.Labels[labelAddonInstance]
		if addonCR == "" || live[addonCR] {
			continue
		}
		size := ""
		if q, ok := p.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
			size = " (" + q.String() + ")"
		}
		out = append(out, Issue{
			Resource:   p.Name,
			Namespace:  p.Namespace,
			Project:    p.Labels["kuso.sislelabs.com/project"],
			Type:       "addon",
			Kind:       KindOrphanAddonPVC,
			Severity:   SeverityWarning,
			Summary:    fmt.Sprintf("Volume %s%s has no KusoAddon %q — data left from a deleted addon.", p.Name, size, addonCR),
			Detail:     "Re-adding an addon with this name is refused until the volume is deleted.",
			Action:     ActionNone,
			Safe:       false,
			Fix:        "Back up anything you still need, then delete the volume.",
			RunbookCmd: fmt.Sprintf("kubectl delete pvc %s -n %s", p.Name, p.Namespace),
		})
	}
	return out
}

// addonPVCs lists addon data PVCs in ns for the orphan sweep.
func (s *Scanner) addonPVCs(ctx context.Context, ns string) ([]corev1.PersistentVolumeClaim, bool) {
	if s.Kube == nil || s.Kube.Clientset == nil {
		return nil, false
	}
	list, err := s.Kube.Clientset.CoreV1().PersistentVolumeClaims(ns).List(ctx, metav1.ListOptions{LabelSelector: addonPVCSelector})
	if err != nil {
		return nil, false
	}
	return list.Items, true
}

// ErrNotOrphan means the resource still has an owning addon (or isn't an
// addon resource at all) and must not be deleted as an orphan.
var ErrNotOrphan = errors.New("reconcilehealth: not an orphan")

// DeleteOrphan re-verifies against the live cluster that the resource
// is an orphan of the given kind, then deletes it. The check is repeated
// here rather than trusting a (cached) report: the addon may have been
// re-created since.
func DeleteOrphan(ctx context.Context, k *kube.Client, kind Kind, ns, name string) error {
	if k == nil || k.Clientset == nil {
		return errors.New("kube client not wired")
	}
	var addonCR string
	switch kind {
	case KindOrphanConnSecret:
		if !strings.HasSuffix(name, connSecretSuffix) || platformConnSecrets[name] {
			return fmt.Errorf("%w: %s is not an addon conn secret", ErrNotOrphan, name)
		}
		if _, err := k.Clientset.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{}); err != nil {
			return fmt.Errorf("get secret: %w", err)
		}
		addonCR = strings.TrimSuffix(name, connSecretSuffix)
	case KindOrphanAddonPVC:
		pvc, err := k.Clientset.CoreV1().PersistentVolumeClaims(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("get pvc: %w", err)
		}
		if pvc.Labels["app.kubernetes.io/name"] != "kusoaddon" || pvc.Labels[labelAddonInstance] == "" {
			return fmt.Errorf("%w: %s is not an addon volume", ErrNotOrphan, name)
		}
		addonCR = pvc.Labels[labelAddonInstance]
	default:
		return fmt.Errorf("%w: unknown kind %q", ErrNotOrphan, kind)
	}
	if _, err := k.GetKusoAddon(ctx, ns, addonCR); err == nil {
		return fmt.Errorf("%w: addon %s still exists", ErrNotOrphan, addonCR)
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("check addon %s: %w", addonCR, err)
	}
	if kind == KindOrphanConnSecret {
		return k.Clientset.CoreV1().Secrets(ns).Delete(ctx, name, metav1.DeleteOptions{})
	}
	return k.Clientset.CoreV1().PersistentVolumeClaims(ns).Delete(ctx, name, metav1.DeleteOptions{})
}
