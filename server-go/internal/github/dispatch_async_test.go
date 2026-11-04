package github

import (
	"context"
	"errors"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuso/server/internal/builds"
)

type chanEmitter chan builds.EventEnvelope

func (c chanEmitter) Emit(e builds.EventEnvelope) { c <- e }

// BUILD-02: the webhook handler records a delivery for dedup in onSuccess.
// A push whose build could not be created (transient apiserver error) must
// not reach onSuccess, or a redelivery from GitHub is swallowed as a replay
// and the commit is never deployed. The failure must also be surfaced.
func TestDispatchAsync_BuildCreateFailure_SkipsOnSuccessAndNotifies(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"ref": "refs/heads/main",
		"after": "abcdef0123456789abcdef0123456789abcdef01",
		"repository": {"full_name": "example/alpha", "default_branch": "main"}
	}`)
	for name, failCreate := range map[string]bool{"create fails": true, "create ok": false} {
		t.Run(name, func(t *testing.T) {
			d := newDispatcher(t,
				seedProj("alpha", "https://github.com/example/alpha", "main", false, 0),
				seedSvc("alpha", "web"),
			)
			if failCreate {
				d.Kube.Dynamic.(*dynamicfake.FakeDynamicClient).PrependReactor("create", "kusobuilds",
					func(k8stesting.Action) (bool, runtime.Object, error) {
						return true, nil, errors.New("etcdserver: request timed out")
					})
			}
			notes := make(chanEmitter, 4)
			d.Notifier = notes
			done := make(chan struct{})
			succeeded := false
			d.DispatchAsync(context.Background(), "push", "guid-1", body, time.Minute, func() { succeeded = true })
			go func() {
				// Wait for the serializer to drain the push's key.
				for {
					d.serial.mu.Lock()
					_, running := d.serial.queues[dispatchKey("push", body)]
					d.serial.mu.Unlock()
					if !running {
						close(done)
						return
					}
					time.Sleep(5 * time.Millisecond)
				}
			}()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("dispatch did not finish")
			}
			if succeeded == failCreate {
				t.Errorf("onSuccess called = %v, want %v", succeeded, !failCreate)
			}
			if failCreate && len(notes) == 0 {
				t.Error("failed dispatch was not surfaced through the notifier")
			}
		})
	}
}
