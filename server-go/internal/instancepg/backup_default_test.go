package instancepg

import (
	"context"
	"io"
	"log/slog"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// Every tenant DB on the managed instance is dumped only by its CronJob, so
// a CR without a backup block gets the default. One the admin set (even an
// empty schedule) is theirs and must not be overwritten or doubled.
func TestEnsureBackupSchedule(t *testing.T) {
	cases := []struct {
		name     string
		backup   map[string]any
		s3Secret bool
		want     any // spec.backup.schedule afterwards; nil = no backup block
	}{
		{"no block, s3 configured", nil, true, "0 3 * * *"},
		{"no block, no s3", nil, false, nil},
		{"admin schedule kept", map[string]any{"schedule": "30 1 * * *"}, true, "30 1 * * *"},
		{"admin disabled kept", map[string]any{"schedule": ""}, true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u := &unstructured.Unstructured{}
			u.SetGroupVersionKind(schema.GroupVersionKind{Group: kube.GVRAddons.Group, Version: kube.GVRAddons.Version, Kind: "KusoAddon"})
			u.SetNamespace("kuso")
			u.SetName(addonCRName())
			spec := map[string]any{"project": instanceProject, "kind": "postgres"}
			if c.backup != nil {
				spec["backup"] = c.backup
			}
			_ = unstructured.SetNestedField(u.Object, spec, "spec")
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
				map[schema.GroupVersionResource]string{kube.GVRAddons: "KusoAddonList"})
			if err := dyn.Tracker().Create(kube.GVRAddons, u, "kuso"); err != nil {
				t.Fatal(err)
			}
			cs := kubefake.NewSimpleClientset()
			if c.s3Secret {
				_, _ = cs.CoreV1().Secrets("kuso").Create(context.Background(),
					&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kuso-backup-s3", Namespace: "kuso"}}, metav1.CreateOptions{})
			}
			s := &Service{Kube: &kube.Client{Dynamic: dyn, Clientset: cs}, Namespace: "kuso", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			addon, err := s.findManagedAddon(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			s.ensureBackupSchedule(context.Background(), addon)

			got, err := dyn.Resource(kube.GVRAddons).Namespace("kuso").Get(context.Background(), addonCRName(), metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			sched, found, _ := unstructured.NestedString(got.Object, "spec", "backup", "schedule")
			if c.want == nil {
				if found {
					t.Fatalf("backup block written without S3: %q", sched)
				}
				return
			}
			if sched != c.want {
				t.Fatalf("schedule = %q, want %q", sched, c.want)
			}
		})
	}
}
