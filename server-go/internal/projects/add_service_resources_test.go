package projects

import (
	"context"
	"reflect"
	"testing"

	"kuso/server/internal/kube"
)

func defaultPodFixture(t *testing.T, def map[string]any) *Service {
	t.Helper()
	s := fakeService(t, seedProject("alpha", kube.KusoProjectSpec{
		DefaultRepo: &kube.KusoRepoRef{URL: "https://github.com/x/y", DefaultBranch: "main"},
	}))
	s.DefaultPodResources = func(context.Context) (map[string]any, error) { return def, nil }
	return s
}

var mediumRes = map[string]any{
	"requests": map[string]any{"cpu": "100m", "memory": "256Mi"},
	"limits":   map[string]any{"memory": "1Gi"},
}

func TestAddService_AppliesDefaultPodSizeWhenNoResources(t *testing.T) {
	t.Parallel()
	for _, rt := range []string{"dockerfile", "worker"} {
		t.Run(rt, func(t *testing.T) {
			s := defaultPodFixture(t, mediumRes)
			req := CreateServiceRequest{Name: "web", Runtime: rt}
			if rt == "worker" {
				req.FromService = "api"
				req.Command = []string{"node", "worker.js"}
			}
			created, err := s.AddService(context.Background(), "alpha", req)
			if err != nil {
				t.Fatalf("AddService: %v", err)
			}
			if !reflect.DeepEqual(created.Spec.Resources, mediumRes) {
				t.Fatalf("service resources = %v, want %v", created.Spec.Resources, mediumRes)
			}
			env, err := s.GetEnvironment(context.Background(), "alpha", "alpha-web-production")
			if err != nil {
				t.Fatalf("production env: %v", err)
			}
			if !reflect.DeepEqual(env.Spec.Resources, mediumRes) {
				t.Fatalf("production env resources = %v, want %v", env.Spec.Resources, mediumRes)
			}
		})
	}
}

func TestAddService_ExplicitResourcesWinOverDefault(t *testing.T) {
	t.Parallel()
	s := defaultPodFixture(t, mediumRes)
	mine := map[string]any{"requests": map[string]any{"memory": "64Mi"}}
	created, err := s.AddService(context.Background(), "alpha", CreateServiceRequest{Name: "web", Runtime: "dockerfile", Resources: &mine})
	if err != nil {
		t.Fatalf("AddService: %v", err)
	}
	if !reflect.DeepEqual(created.Spec.Resources, mine) {
		t.Fatalf("resources = %v, want the request's %v", created.Spec.Resources, mine)
	}
}

// An explicit empty map opts this one service out of the default.
func TestAddService_ExplicitEmptyResourcesSkipsDefault(t *testing.T) {
	t.Parallel()
	s := defaultPodFixture(t, mediumRes)
	empty := map[string]any{}
	created, err := s.AddService(context.Background(), "alpha", CreateServiceRequest{Name: "web", Runtime: "dockerfile", Resources: &empty})
	if err != nil {
		t.Fatalf("AddService: %v", err)
	}
	if created.Spec.Resources != nil {
		t.Fatalf("resources = %v, want none", created.Spec.Resources)
	}
}

// default pod size "none" → the hook yields nil → no resources.
func TestAddService_NoDefaultLeavesResourcesEmpty(t *testing.T) {
	t.Parallel()
	s := defaultPodFixture(t, nil)
	created, err := s.AddService(context.Background(), "alpha", CreateServiceRequest{Name: "web", Runtime: "dockerfile"})
	if err != nil {
		t.Fatalf("AddService: %v", err)
	}
	if created.Spec.Resources != nil {
		t.Fatalf("resources = %v, want none", created.Spec.Resources)
	}
}

var largeRes = map[string]any{
	"requests": map[string]any{"cpu": "250m", "memory": "512Mi"},
	"limits":   map[string]any{"memory": "2Gi"},
}

func sizedFixture(t *testing.T) *Service {
	t.Helper()
	s := defaultPodFixture(t, mediumRes)
	s.PodSizeResources = func(_ context.Context, name string) (map[string]any, bool, error) {
		if name == "large" {
			return largeRes, true, nil
		}
		return nil, false, nil
	}
	return s
}

// A named size (e.g. a marketplace app's preset) beats the instance default.
func TestAddService_NamedSizeBeatsDefault(t *testing.T) {
	t.Parallel()
	s := sizedFixture(t)
	created, err := s.AddService(context.Background(), "alpha", CreateServiceRequest{Name: "web", Runtime: "dockerfile", Size: "large"})
	if err != nil {
		t.Fatalf("AddService: %v", err)
	}
	if !reflect.DeepEqual(created.Spec.Resources, largeRes) {
		t.Fatalf("resources = %v, want large %v", created.Spec.Resources, largeRes)
	}
}

func TestAddService_ExplicitResourcesBeatNamedSize(t *testing.T) {
	t.Parallel()
	s := sizedFixture(t)
	mine := map[string]any{"requests": map[string]any{"memory": "64Mi"}}
	created, err := s.AddService(context.Background(), "alpha", CreateServiceRequest{Name: "web", Runtime: "dockerfile", Size: "large", Resources: &mine})
	if err != nil {
		t.Fatalf("AddService: %v", err)
	}
	if !reflect.DeepEqual(created.Spec.Resources, mine) {
		t.Fatalf("resources = %v, want %v", created.Spec.Resources, mine)
	}
}

// A preset the admin deleted falls back to the default rather than failing
// the create.
func TestAddService_UnknownSizeFallsBackToDefault(t *testing.T) {
	t.Parallel()
	s := sizedFixture(t)
	created, err := s.AddService(context.Background(), "alpha", CreateServiceRequest{Name: "web", Runtime: "dockerfile", Size: "gone"})
	if err != nil {
		t.Fatalf("AddService: %v", err)
	}
	if !reflect.DeepEqual(created.Spec.Resources, mediumRes) {
		t.Fatalf("resources = %v, want default %v", created.Spec.Resources, mediumRes)
	}
}
