package addons

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Admin commands (browser role, password repair) exec into the addon's
// postgres pod. They assumed the StatefulSet pod <addon>-0, which an HA
// (CloudNativePG) addon doesn't have: its pods are <addon>-1..N and the
// primary moves on failover (live: rs/hadb SQL browser 502, "pods rs-hadb-0
// not found"). On CNPG pods psql must run as the postgres superuser.
func TestPostgresExecTarget(t *testing.T) {
	ctx := context.Background()
	pod := func(name string, labels map[string]string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", Labels: labels}}
	}

	s := fakeServiceWithSecrets(t, seedProj("p"))
	cs := s.Kube.Clientset.CoreV1()
	_, _ = cs.Pods("ns").Create(ctx, pod("p-db-0", nil), metav1.CreateOptions{})
	for i, role := range []string{"replica", "primary", "replica"} {
		name := []string{"p-hadb-1", "p-hadb-2", "p-hadb-3"}[i]
		_, _ = cs.Pods("ns").Create(ctx, pod(name, map[string]string{
			"cnpg.io/cluster": "p-hadb", "cnpg.io/instanceRole": role,
		}), metav1.CreateOptions{})
	}

	pod0, user, err := s.postgresExecTarget(ctx, "ns", "p-db", "kuso")
	if err != nil || pod0 != "p-db-0" || user != "kuso" {
		t.Errorf("StatefulSet addon: got %q %q %v, want p-db-0 as kuso", pod0, user, err)
	}
	primary, user, err := s.postgresExecTarget(ctx, "ns", "p-hadb", "kuso")
	if err != nil || primary != "p-hadb-2" || user != "postgres" {
		t.Errorf("CNPG addon: got %q %q %v, want the primary p-hadb-2 as postgres", primary, user, err)
	}
	if _, _, err := s.postgresExecTarget(ctx, "ns", "p-missing", "kuso"); err == nil {
		t.Error("no pod: want an error naming the addon")
	}
}
