package addons

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/kube"
)

// connSecretData returns a conn Secret's data, or nil when it can't be read.
func (s *Service) connSecretData(ctx context.Context, ns, name string) map[string][]byte {
	sec, err := s.Kube.Clientset.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil
	}
	return sec.Data
}

func connDataEqual(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		w, ok := b[k]
		if !ok || !bytes.Equal(v, w) {
			return false
		}
	}
	return true
}

// restartConnConsumers rolls every env in ns that reads connName, through
// envFrom or a secretKeyRef, so pods pick up changed credentials. Pods only
// resolve those at container start. Best-effort: a failed roll is logged
// and the caller's write still stands.
func (s *Service) restartConnConsumers(ctx context.Context, ns, connName string) {
	envs, err := s.Kube.ListKusoEnvironments(ctx, ns)
	if err != nil {
		slog.Default().Warn("addons: list envs to restart conn consumers", "conn", connName, "err", err)
		return
	}
	at := time.Now().UTC().Format(time.RFC3339)
	patch := fmt.Appendf(nil, `{"spec":{"template":{"metadata":{"annotations":{"kuso.sislelabs.com/restartedAt":%q}}}}}`, at)
	for _, e := range envs {
		if !envReadsConn(e.Spec.EnvFromSecrets, e.Spec.EnvVars, connName) {
			continue
		}
		if _, err := s.Kube.Clientset.AppsV1().Deployments(ns).Patch(ctx, e.Name, types.StrategicMergePatchType, patch, metav1.PatchOptions{}); err != nil {
			slog.Default().Warn("addons: restart conn consumer", "env", e.Name, "conn", connName, "err", err)
		}
	}
}

func envReadsConn(envFrom []string, vars []kube.KusoEnvVar, connName string) bool {
	if slices.Contains(envFrom, connName) {
		return true
	}
	for _, v := range vars {
		ref, _ := v.ValueFrom["secretKeyRef"].(map[string]any)
		if name, _ := ref["name"].(string); name == connName {
			return true
		}
	}
	return false
}
