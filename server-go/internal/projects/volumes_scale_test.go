package projects

import (
	"context"
	"errors"
	"testing"

	"kuso/server/internal/kube"
)

// BLOB-9: a ReadWriteOnce volume on a service that runs, or autoscales
// to, more than one replica gives the extra pods the same disk.
func TestPatchService_RejectsSingleWriterVolumeOnScaledService(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	one, three := 1, 3
	newSvc := func(scaleMax int) *Service {
		sc := &kube.KusoScaleSpec{Max: scaleMax}
		sc.SetMin(1)
		return fakeService(t,
			seedProject("alpha", kube.KusoProjectSpec{BaseDomain: "alpha.example.com"}),
			seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Port: 3000, Scale: sc}),
		)
	}
	rwo := []VolumePatch{{Name: "data", MountPath: "/data", SizeGi: 1}}
	if _, err := newSvc(5).PatchService(ctx, "alpha", "web", PatchServiceRequest{Volumes: &rwo}); !errors.Is(err, ErrInvalid) {
		t.Errorf("RWO volume on an autoscaled service: err=%v, want invalid", err)
	}
	rwx := []VolumePatch{{Name: "data", MountPath: "/data", SizeGi: 1, AccessMode: "RWX"}}
	if _, err := newSvc(5).PatchService(ctx, "alpha", "web", PatchServiceRequest{Volumes: &rwx}); err != nil {
		t.Errorf("RWX volume on an autoscaled service: %v", err)
	}
	s := newSvc(1)
	if _, err := s.PatchService(ctx, "alpha", "web", PatchServiceRequest{Volumes: &rwo}); err != nil {
		t.Fatalf("RWO volume on a single replica: %v", err)
	}
	if _, err := s.PatchService(ctx, "alpha", "web", PatchServiceRequest{Scale: &PatchScaleRequest{Min: &one, Max: &three}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("scaling a service with an RWO volume to 3: err=%v, want invalid", err)
	}
}

func TestAddService_SingleWriterVolumeDefaultsToOneReplica(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := fakeService(t, seedProject("alpha", kube.KusoProjectSpec{
		DefaultRepo: &kube.KusoRepoRef{URL: "https://github.com/x/y", DefaultBranch: "main"},
		BaseDomain:  "alpha.example.com",
	}))
	svc, err := s.AddService(ctx, "alpha", CreateServiceRequest{
		Name: "web", Runtime: "dockerfile", Port: 3000,
		Volumes: []VolumePatch{{Name: "data", MountPath: "/data", SizeGi: 1}},
	})
	if err != nil {
		t.Fatalf("AddService: %v", err)
	}
	if svc.Spec.Scale == nil || svc.Spec.Scale.Max > 1 {
		t.Errorf("scale = %+v, want a single replica", svc.Spec.Scale)
	}
	if _, err := s.AddService(ctx, "alpha", CreateServiceRequest{
		Name: "api", Runtime: "dockerfile", Port: 3000, Scale: &ServiceScale{Max: 4},
		Volumes: []VolumePatch{{Name: "data", MountPath: "/data", SizeGi: 1}},
	}); !errors.Is(err, ErrInvalid) {
		t.Errorf("explicit max 4 with an RWO volume: err=%v, want invalid", err)
	}
}
