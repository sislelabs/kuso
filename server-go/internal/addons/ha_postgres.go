package addons

import (
	"context"
	"fmt"
	"log/slog"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"kuso/server/internal/kube"
)

// GVRCNPGCluster is the CloudNativePG Cluster an HA Postgres addon renders.
var GVRCNPGCluster = schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "clusters"}

// haClusterName returns the addon's CNPG Cluster name when one exists
// and was rendered by the kusoaddon chart, "" otherwise. The label check
// keeps this off clusters kuso didn't create for an addon (the control
// plane's own CNPG database lives in the same namespace). A cluster
// without the CNPG CRD installed answers NotFound, which is "none".
func haClusterName(ctx context.Context, k *kube.Client, ns, fqn string) (string, error) {
	if k == nil || k.Dynamic == nil {
		return "", nil
	}
	c, err := k.Dynamic.Resource(GVRCNPGCluster).Namespace(ns).Get(ctx, fqn, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get CNPG cluster %s: %w", fqn, err)
	}
	if c.GetLabels()["app.kubernetes.io/name"] != "kusoaddon" || c.GetLabels()["app.kubernetes.io/instance"] != fqn {
		return "", nil
	}
	if c.GetDeletionTimestamp() != nil {
		return "", nil
	}
	return c.GetName(), nil
}

// PurgeHAPostgres deletes an HA Postgres addon's CNPG Cluster and its
// <fqn>-app password Secret. The chart marks both helm resource-policy:
// keep, so an addon delete leaves them (and the replica PVCs they own)
// running. Deleting the Cluster cascades its PVCs through their
// ownerReferences; dropping -app stops a re-add from adopting the old
// password. No-op for an addon that never ran HA.
func PurgeHAPostgres(ctx context.Context, k *kube.Client, ns, fqn string) error {
	name, err := haClusterName(ctx, k, ns, fqn)
	if err != nil {
		return err
	}
	if name == "" {
		return nil
	}
	bg := metav1.DeletePropagationBackground
	if err := k.Dynamic.Resource(GVRCNPGCluster).Namespace(ns).
		Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &bg}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete CNPG cluster %s: %w", name, err)
	}
	if k.Clientset != nil {
		if err := k.Clientset.CoreV1().Secrets(ns).Delete(ctx, fqn+"-app", metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete %s-app secret: %w", fqn, err)
		}
	}
	return nil
}

func (s *Service) purgeHAPostgres(ctx context.Context, ns, fqn string) {
	if err := PurgeHAPostgres(ctx, s.Kube, ns, fqn); err != nil {
		slog.Default().Warn("addon purge: HA Postgres cluster left behind", "addon", fqn, "namespace", ns, "err", err)
	}
}
