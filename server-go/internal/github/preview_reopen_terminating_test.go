package github

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

const reopenPR42 = `{
	"action": "reopened",
	"number": 42,
	"pull_request": {
		"head": {"ref": "feat/x", "sha": "abcdef0123456789abcdef0123456789abcdef01"},
		"base": {"ref": "main"},
		"state": "open"
	},
	"repository": {"full_name": "example/alpha"}
}`

func shrinkTeardownWait(t *testing.T, wait time.Duration) {
	oldW, oldP := previewTeardownWait, previewTeardownPoll
	previewTeardownWait, previewTeardownPoll = wait, 10*time.Millisecond
	t.Cleanup(func() { previewTeardownWait, previewTeardownPoll = oldW, oldP })
}

// SE-08 / B-24: PR closed then reopened while the old preview env is
// still in its helm-uninstall finalizer window. The reopen must wait for
// the delete and create a fresh env, not update the dying one.
func TestDispatch_PRReopenWaitsForTerminatingEnv(t *testing.T) {
	shrinkTeardownWait(t, 5*time.Second)
	terminating := seedPreviewEnv("alpha", "web", 42, "feat/x")
	now := metav1.Now()
	terminating.obj.SetDeletionTimestamp(&now)
	terminating.obj.SetFinalizers([]string{"uninstall-helm-release"})
	d := newDispatcher(t,
		seedProj("alpha", "https://github.com/example/alpha", "main", true, 5),
		seedSvc("alpha", "web"),
		terminating,
	)
	ctx := context.Background()
	gone := make(chan struct{})
	go func() {
		defer close(gone)
		time.Sleep(150 * time.Millisecond)
		if err := d.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace("kuso").
			Delete(ctx, "alpha-web-pr-42", metav1.DeleteOptions{}); err != nil {
			t.Errorf("finalizer finish: %v", err)
		}
	}()
	if err := d.Dispatch(ctx, "pull_request", []byte(reopenPR42)); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	<-gone
	env, err := d.Kube.GetKusoEnvironment(ctx, "kuso", "alpha-web-pr-42")
	if err != nil {
		t.Fatalf("preview env missing after reopen (updated the terminating CR instead of recreating): %v", err)
	}
	if env.DeletionTimestamp != nil {
		t.Fatalf("preview env is the terminating CR, not a fresh one")
	}
}

type countingPreviewDB struct{ ensured int }

func (r *countingPreviewDB) EnsurePRAddons(context.Context, string, int) ([]string, map[string]string, error) {
	r.ensured++
	return nil, map[string]string{}, nil
}
func (r *countingPreviewDB) DeletePRAddons(context.Context, string, int) error { return nil }

// A clone addon still terminating must not be reused by the reopen.
func TestDispatch_PRReopenDoesNotReuseTerminatingClone(t *testing.T) {
	shrinkTeardownWait(t, 50*time.Millisecond)
	now := metav1.Now()
	clone := typedSeed(kube.GVRAddons, "KusoAddon", &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{
			Name: "alpha-db-pr-42", Namespace: "kuso",
			Labels: map[string]string{
				kube.LabelProject:               "alpha",
				"kuso.sislelabs.com/preview-pr": "42",
			},
			DeletionTimestamp: &now,
			Finalizers:        []string{"uninstall-helm-release"},
		},
	})
	d := newDispatcher(t,
		seedProj("alpha", "https://github.com/example/alpha", "main", true, 5),
		seedSvc("alpha", "web"),
		clone,
	)
	pdb := &countingPreviewDB{}
	d.PreviewDB = pdb
	if err := d.Dispatch(context.Background(), "pull_request", []byte(reopenPR42)); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if pdb.ensured != 0 {
		t.Errorf("EnsurePRAddons ran against a terminating clone (%d calls)", pdb.ensured)
	}
}
