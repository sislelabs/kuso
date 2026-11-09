package projects

import (
	"context"
	"errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuso/server/internal/kube"
)

// The service spec write is the durable change; env propagation is
// best-effort. A propagation failure used to return before RecordRevision,
// so the edit landed on the CR but never showed up in `kuso revision list`.
func TestPatchService_RecordsRevisionEvenWhenPropagationFails(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{Port: 8080}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)
	dyn := s.Kube.Dynamic.(*dynamicfake.FakeDynamicClient)
	for _, verb := range []string{"update", "patch"} {
		dyn.PrependReactor(verb, kube.GVREnvironments.Resource, func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("apiserver hiccup")
		})
	}
	var recorded []string
	s.RecordRevision = func(_ context.Context, project, kind, name, summary string, _ []byte) {
		recorded = append(recorded, project+"/"+kind+"/"+name)
	}

	port := int32(3000)
	if _, err := s.PatchService(context.Background(), "alpha", "web", PatchServiceRequest{Port: &port}); err != nil {
		t.Fatalf("PatchService: %v", err)
	}
	if len(recorded) != 1 || recorded[0] != "alpha/service/web" {
		t.Fatalf("recorded revisions = %v, want [alpha/service/web]", recorded)
	}
}

// A rollback from History replays an old revision; the revision it records
// must say so, or the release list can't tell a rollback from a hand edit.
func TestPatchService_RecordsRevertOf(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{Port: 8080}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)
	var snaps []string
	s.RecordRevision = func(_ context.Context, _, _, _, _ string, snap []byte) { snaps = append(snaps, string(snap)) }
	port := int32(3000)
	if _, err := s.PatchService(WithRevertOf(context.Background(), "rev-42"), "alpha", "web", PatchServiceRequest{Port: &port}); err != nil {
		t.Fatalf("PatchService: %v", err)
	}
	if _, err := s.PatchService(context.Background(), "alpha", "web", PatchServiceRequest{Port: &port}); err != nil {
		t.Fatalf("PatchService: %v", err)
	}
	if len(snaps) != 2 || !strings.Contains(snaps[0], `"revertOf":"rev-42"`) || strings.Contains(snaps[1], "revertOf") {
		t.Fatalf("snapshots = %v", snaps)
	}
}
