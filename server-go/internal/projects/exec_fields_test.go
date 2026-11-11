package projects

import (
	"slices"
	"testing"

	"kuso/server/internal/kube"
)

// A settings save or kuso.yml re-apply re-sends unchanged values (and
// masked build args); only a real change to an execution-bearing field
// may count, or every editor save of an unrelated field would 403.
func TestExecPatchFields_OnlyRealChanges(t *testing.T) {
	cur := &kube.KusoServiceSpec{
		Image:     &kube.KusoImage{Repository: "ghcr.io/acme/web", Tag: "latest"},
		BuildArgs: map[string]string{"TOKEN": "abc"},
	}
	str := func(s string) *string { return &s }
	same := PatchServiceRequest{
		Runtime:   str("dockerfile"),
		Image:     &ServiceImageSpec{Repository: "ghcr.io/acme/web"},
		BuildArgs: &map[string]string{"TOKEN": EnvMaskSentinel},
	}
	if got := ExecPatchFields(cur, same); len(got) != 0 {
		t.Errorf("unchanged values flagged: %v", got)
	}
	changed := PatchServiceRequest{
		Image:     &ServiceImageSpec{Repository: "ghcr.io/acme/web", Tag: "2"},
		BuildArgs: &map[string]string{"NIXPACKS_BUILD_CMD": "printenv"},
	}
	got := ExecPatchFields(cur, changed)
	if !slices.Contains(got, ExecFieldImage) || !slices.Contains(got, ExecFieldBuildArgs) {
		t.Errorf("ExecPatchFields = %v, want image and buildArgs", got)
	}
}
