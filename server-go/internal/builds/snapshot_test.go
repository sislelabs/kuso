package builds

import (
	"context"
	"errors"
	"testing"

	"kuso/server/internal/kube"
)

type fakeSnapshotter struct {
	called bool
	keys   []string
	err    error
}

func (f *fakeSnapshotter) Snapshot(ctx context.Context, ns string, env *kube.KusoEnvironment) ([]string, error) {
	f.called = true
	return f.keys, f.err
}

func TestSnapshotDecision(t *testing.T) {
	env := &kube.KusoEnvironment{}
	env.Spec.SnapshotBeforeDeploy = true
	env.Spec.Release = &kube.KusoReleaseSpec{Command: []string{"migrate"}}

	// flag on + hook present -> should snapshot
	if !shouldSnapshot(env, &fakeSnapshotter{}) {
		t.Error("expected snapshot when flag on + hook present + snapshotter set")
	}
	// flag off -> no snapshot
	env.Spec.SnapshotBeforeDeploy = false
	if shouldSnapshot(env, &fakeSnapshotter{}) {
		t.Error("flag off should mean no snapshot")
	}
	// flag on, no snapshotter -> no snapshot
	env.Spec.SnapshotBeforeDeploy = true
	if shouldSnapshot(env, nil) {
		t.Error("nil snapshotter should mean no snapshot")
	}
	// flag on, no release hook -> no snapshot
	env.Spec.Release = nil
	if shouldSnapshot(env, &fakeSnapshotter{}) {
		t.Error("no release hook should mean no snapshot")
	}
}

func TestSnapshotInfraFailBlocks(t *testing.T) {
	fs := &fakeSnapshotter{err: errors.New("s3 down")}
	_, err := runPredeploySnapshot(context.Background(), "ns", &kube.KusoEnvironment{}, fs)
	if err == nil {
		t.Fatal("snapshot infra error must propagate so the caller blocks the deploy")
	}
	if !fs.called {
		t.Fatal("snapshotter should have been called")
	}
}
