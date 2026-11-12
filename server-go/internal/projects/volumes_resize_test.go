package projects

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

func volumePVCObj(env, vol, size, sc string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: env + "-" + vol, Namespace: "kuso", Labels: map[string]string{
			"app.kubernetes.io/instance": env, "kuso.sislelabs.com/volume": vol, labelProject: "alpha",
		}},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: &sc,
			Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{
				corev1.ResourceStorage: resource.MustParse(size),
			}},
		},
	}
}

// BLOB-2: a volume size the PVC can't take (a shrink, the 1Gi default on
// a re-add, growth on a non-expandable StorageClass) wedges the env's helm
// release, so the API refuses it or keeps the disk's size.
func TestPatchService_VolumeSizeMatchesExistingDisk(t *testing.T) {
	t.Parallel()
	yes := true
	newSvc := func(vols []kube.KusoVolume) *Service {
		s := fakeService(t,
			seedProject("alpha", kube.KusoProjectSpec{BaseDomain: "alpha.example.com"}),
			seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Port: 3000, Volumes: vols}),
			seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
		)
		s.Kube.Clientset = kubefake.NewSimpleClientset(
			volumePVCObj("alpha-web-production", "uploads", "5Gi", "local-path"),
			volumePVCObj("alpha-web-production", "cache", "2Gi", "longhorn"),
			volumePVCObj("other-env", "uploads", "50Gi", "local-path"),
			&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "local-path"}},
			&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "longhorn"}, AllowVolumeExpansion: &yes},
		)
		return s
	}
	ctx := context.Background()
	patch := func(s *Service, vols ...VolumePatch) (*kube.KusoService, error) {
		return s.PatchService(ctx, "alpha", "web", PatchServiceRequest{Volumes: &vols})
	}
	sizeOf := func(svc *kube.KusoService, name string) int {
		for _, v := range svc.Spec.Volumes {
			if v.Name == name {
				return v.SizeGi
			}
		}
		return -1
	}

	// Re-add after `volume rm` without --size keeps the retained 5Gi.
	svc, err := patch(newSvc(nil), VolumePatch{Name: "uploads", MountPath: "/app/uploads"})
	if err != nil || sizeOf(svc, "uploads") != 5 {
		t.Fatalf("re-add: size=%d err=%v, want 5", sizeOf(svc, "uploads"), err)
	}

	existing := []kube.KusoVolume{{Name: "uploads", MountPath: "/app/uploads", SizeGi: 5}, {Name: "cache", MountPath: "/cache", SizeGi: 2}}
	// A client echoing the list without sizes keeps them.
	svc, err = patch(newSvc(existing), VolumePatch{Name: "uploads", MountPath: "/app/uploads"}, VolumePatch{Name: "cache", MountPath: "/cache"})
	if err != nil || sizeOf(svc, "uploads") != 5 || sizeOf(svc, "cache") != 2 {
		t.Fatalf("echo: %+v err=%v", svc, err)
	}
	if _, err := patch(newSvc(existing), VolumePatch{Name: "uploads", MountPath: "/app/uploads", SizeGi: 3}); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "only grow") {
		t.Errorf("shrink: err=%v, want a conflict", err)
	}
	if _, err := patch(newSvc(existing), VolumePatch{Name: "uploads", MountPath: "/app/uploads", SizeGi: 10}); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "cannot be expanded") {
		t.Errorf("grow on local-path: err=%v, want a conflict", err)
	}
	svc, err = patch(newSvc(existing), VolumePatch{Name: "cache", MountPath: "/cache", SizeGi: 8})
	if err != nil || sizeOf(svc, "cache") != 8 {
		t.Errorf("grow on an expandable class: size=%d err=%v", sizeOf(svc, "cache"), err)
	}
	// A brand-new volume has no disk to compare against.
	if _, err := patch(newSvc(nil), VolumePatch{Name: "data", MountPath: "/data", SizeGi: 1}); err != nil {
		t.Errorf("new volume: %v", err)
	}
}
