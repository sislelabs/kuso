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

func TestAddService_PodResources(t *testing.T) {
	t.Parallel()
	mine := map[string]any{"requests": map[string]any{"memory": "64Mi"}}
	empty := map[string]any{}
	cases := []struct {
		name    string
		def     map[string]any // instance default; ignored when sized
		sized   bool
		runtime string
		size    string
		res     *map[string]any
		want    map[string]any
		// checkEnv also asserts the production env carries the resources.
		checkEnv bool
	}{
		{name: "AppliesDefaultPodSizeWhenNoResources/dockerfile", def: mediumRes, runtime: "dockerfile", want: mediumRes, checkEnv: true},
		{name: "AppliesDefaultPodSizeWhenNoResources/worker", def: mediumRes, runtime: "worker", want: mediumRes, checkEnv: true},
		{name: "ExplicitResourcesWinOverDefault", def: mediumRes, runtime: "dockerfile", res: &mine, want: mine},
		// An explicit empty map opts this one service out of the default.
		{name: "ExplicitEmptyResourcesSkipsDefault", def: mediumRes, runtime: "dockerfile", res: &empty, want: nil},
		// default pod size "none" → the hook yields nil → no resources.
		{name: "NoDefaultLeavesResourcesEmpty", def: nil, runtime: "dockerfile", want: nil},
		// A named size (e.g. a marketplace app's preset) beats the instance default.
		{name: "NamedSizeBeatsDefault", sized: true, runtime: "dockerfile", size: "large", want: largeRes},
		{name: "ExplicitResourcesBeatNamedSize", sized: true, runtime: "dockerfile", size: "large", res: &mine, want: mine},
		// A preset the admin deleted falls back to the default rather than
		// failing the create.
		{name: "UnknownSizeFallsBackToDefault", sized: true, runtime: "dockerfile", size: "gone", want: mediumRes},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var s *Service
			if tc.sized {
				s = sizedFixture(t)
			} else {
				s = defaultPodFixture(t, tc.def)
			}
			req := CreateServiceRequest{Name: "web", Runtime: tc.runtime, Size: tc.size, Resources: tc.res}
			if tc.runtime == "worker" {
				req.FromService = "api"
				req.Command = []string{"node", "worker.js"}
			}
			created, err := s.AddService(context.Background(), "alpha", req)
			if err != nil {
				t.Fatalf("AddService: %v", err)
			}
			if tc.want == nil {
				if created.Spec.Resources != nil {
					t.Fatalf("resources = %v, want none", created.Spec.Resources)
				}
			} else if !reflect.DeepEqual(created.Spec.Resources, tc.want) {
				t.Fatalf("service resources = %v, want %v", created.Spec.Resources, tc.want)
			}
			if !tc.checkEnv {
				return
			}
			env, err := s.GetEnvironment(context.Background(), "alpha", "alpha-web-production")
			if err != nil {
				t.Fatalf("production env: %v", err)
			}
			if !reflect.DeepEqual(env.Spec.Resources, tc.want) {
				t.Fatalf("production env resources = %v, want %v", env.Spec.Resources, tc.want)
			}
		})
	}
}
