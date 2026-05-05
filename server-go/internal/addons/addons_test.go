package addons

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"kuso/server/internal/kube"
)

type seed struct {
	gvr schema.GroupVersionResource
	obj *unstructured.Unstructured
}

func fakeService(t *testing.T, seeds ...seed) *Service {
	t.Helper()
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		kube.GVRProjects:     "KusoProjectList",
		kube.GVREnvironments: "KusoEnvironmentList",
		kube.GVRAddons:       "KusoAddonList",
	})
	for _, s := range seeds {
		if err := dyn.Tracker().Create(s.gvr, s.obj, "kuso"); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	return &Service{Kube: &kube.Client{Dynamic: dyn}, Namespace: "kuso"}
}

func typedSeed(gvr schema.GroupVersionResource, kind string, obj any) seed {
	m, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(gvr.GroupVersion().WithKind(kind))
	if u.GetNamespace() == "" {
		u.SetNamespace("kuso")
	}
	return seed{gvr: gvr, obj: u}
}

func seedProj(name string) seed {
	return typedSeed(kube.GVRProjects, "KusoProject", &kube.KusoProject{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso"},
		Spec:       kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}},
	})
}

func seedEnv(project, service, kind, name string) seed {
	return typedSeed(kube.GVREnvironments, "KusoEnvironment", &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "kuso",
			Labels: map[string]string{
				"kuso.sislelabs.com/project": project,
				"kuso.sislelabs.com/service": service,
				"kuso.sislelabs.com/env":     kind,
			},
		},
		Spec: kube.KusoEnvironmentSpec{Project: project, Service: project + "-" + service, Kind: kind},
	})
}

func TestAddonName(t *testing.T) {
	t.Parallel()
	if got := addonCRName("alpha", "pg"); got != "alpha-pg" {
		t.Errorf("addonCRName: %q", got)
	}
	if got := addonCRName("alpha", "alpha-pg"); got != "alpha-pg" {
		t.Errorf("addonCRName already-prefixed: %q", got)
	}
	if got := connSecretName("alpha-pg"); got != "alpha-pg-conn" {
		t.Errorf("connSecretName: %q", got)
	}
}

func TestAdd_RefreshesEnvSecrets(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProj("alpha"),
		seedEnv("alpha", "web", "production", "alpha-web-production"),
	)

	if _, err := s.Add(context.Background(), "alpha", CreateAddonRequest{Name: "pg", Kind: "postgres"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	envCR, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-production")
	if err != nil {
		t.Fatalf("get env: %v", err)
	}
	if len(envCR.Spec.EnvFromSecrets) != 1 || envCR.Spec.EnvFromSecrets[0] != "alpha-pg-conn" {
		t.Errorf("envFromSecrets not patched: %+v", envCR.Spec.EnvFromSecrets)
	}
}

func TestAdd_DuplicateRejected(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProj("alpha"),
		typedSeed(kube.GVRAddons, "KusoAddon", &kube.KusoAddon{
			ObjectMeta: metav1.ObjectMeta{Name: "alpha-pg", Namespace: "kuso"},
			Spec:       kube.KusoAddonSpec{Project: "alpha", Kind: "postgres"},
		}),
	)
	_, err := s.Add(context.Background(), "alpha", CreateAddonRequest{Name: "pg", Kind: "postgres"})
	if !errors.Is(err, ErrConflict) {
		t.Errorf("got %v", err)
	}
}

func TestAdd_MissingFields(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProj("alpha"))
	_, err := s.Add(context.Background(), "alpha", CreateAddonRequest{Name: "pg"}) // missing kind
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("got %v", err)
	}
}

func TestDelete_RefreshesEnvSecrets(t *testing.T) {
	t.Parallel()
	// Two addons; delete one and confirm env secret list shrinks to the
	// remaining one.
	s := fakeService(t,
		seedProj("alpha"),
		typedSeed(kube.GVRAddons, "KusoAddon", &kube.KusoAddon{
			ObjectMeta: metav1.ObjectMeta{Name: "alpha-pg", Namespace: "kuso", Labels: map[string]string{"kuso.sislelabs.com/project": "alpha"}},
			Spec:       kube.KusoAddonSpec{Project: "alpha", Kind: "postgres"},
		}),
		typedSeed(kube.GVRAddons, "KusoAddon", &kube.KusoAddon{
			ObjectMeta: metav1.ObjectMeta{Name: "alpha-redis", Namespace: "kuso", Labels: map[string]string{"kuso.sislelabs.com/project": "alpha"}},
			Spec:       kube.KusoAddonSpec{Project: "alpha", Kind: "redis"},
		}),
		seedEnv("alpha", "web", "production", "alpha-web-production"),
	)

	if err := s.Delete(context.Background(), "alpha", "pg"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	envCR, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-production")
	if err != nil {
		t.Fatalf("get env: %v", err)
	}
	if len(envCR.Spec.EnvFromSecrets) != 1 || envCR.Spec.EnvFromSecrets[0] != "alpha-redis-conn" {
		t.Errorf("envFromSecrets after delete: %+v", envCR.Spec.EnvFromSecrets)
	}
}

func TestDelete_NotFound(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProj("alpha"))
	if err := s.Delete(context.Background(), "alpha", "ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v", err)
	}
}

func seedAddon(project, name, kind string) seed {
	return typedSeed(kube.GVRAddons, "KusoAddon", &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{Name: project + "-" + name, Namespace: "kuso"},
		Spec:       kube.KusoAddonSpec{Project: project, Kind: kind},
	})
}

// TestUpdate_BackupRoundTrip covers the v0.7.53 addition: backup
// schedule + retention are settable via UpdateAddonRequest, no longer
// kubectl-patch-only. Three sub-cases exercise the validator + the
// disable path.
func TestUpdate_BackupRoundTrip(t *testing.T) {
	t.Parallel()
	t.Run("set schedule + retention", func(t *testing.T) {
		s := fakeService(t,
			seedProj("alpha"),
			seedAddon("alpha", "pg", "postgres"),
		)
		sched, ret := "0 3 * * *", 14
		got, err := s.Update(context.Background(), "alpha", "pg", UpdateAddonRequest{
			Backup: &UpdateBackupPatch{Schedule: &sched, RetentionDays: &ret},
		})
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
		if got.Spec.Backup == nil || got.Spec.Backup.Schedule != "0 3 * * *" || got.Spec.Backup.RetentionDays != 14 {
			t.Errorf("backup not persisted: %+v", got.Spec.Backup)
		}
	})

	t.Run("empty schedule disables", func(t *testing.T) {
		s := fakeService(t,
			seedProj("alpha"),
			typedSeed(kube.GVRAddons, "KusoAddon", &kube.KusoAddon{
				ObjectMeta: metav1.ObjectMeta{Name: "alpha-pg", Namespace: "kuso"},
				Spec: kube.KusoAddonSpec{
					Project: "alpha",
					Kind:    "postgres",
					Backup:  &kube.KusoBackup{Schedule: "0 3 * * *", RetentionDays: 14},
				},
			}),
		)
		empty := ""
		got, err := s.Update(context.Background(), "alpha", "pg", UpdateAddonRequest{
			Backup: &UpdateBackupPatch{Schedule: &empty},
		})
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
		if got.Spec.Backup.Schedule != "" {
			t.Errorf("schedule not cleared: %q", got.Spec.Backup.Schedule)
		}
		if got.Spec.Backup.RetentionDays != 14 {
			t.Errorf("retention got clobbered: %d", got.Spec.Backup.RetentionDays)
		}
	})

	t.Run("malformed schedule rejected", func(t *testing.T) {
		s := fakeService(t,
			seedProj("alpha"),
			seedAddon("alpha", "pg", "postgres"),
		)
		bad := "every monday"
		_, err := s.Update(context.Background(), "alpha", "pg", UpdateAddonRequest{
			Backup: &UpdateBackupPatch{Schedule: &bad},
		})
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("want ErrInvalid, got %v", err)
		}
	})

	t.Run("retention out of range", func(t *testing.T) {
		s := fakeService(t,
			seedProj("alpha"),
			seedAddon("alpha", "pg", "postgres"),
		)
		neg := -1
		if _, err := s.Update(context.Background(), "alpha", "pg", UpdateAddonRequest{
			Backup: &UpdateBackupPatch{RetentionDays: &neg},
		}); !errors.Is(err, ErrInvalid) {
			t.Errorf("negative: want ErrInvalid, got %v", err)
		}
		huge := 99999
		if _, err := s.Update(context.Background(), "alpha", "pg", UpdateAddonRequest{
			Backup: &UpdateBackupPatch{RetentionDays: &huge},
		}); !errors.Is(err, ErrInvalid) {
			t.Errorf("huge: want ErrInvalid, got %v", err)
		}
	})
}
