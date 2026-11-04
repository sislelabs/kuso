package github

import (
	"context"
	"errors"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuso/server/internal/builds"
)

// LIFE-12: a transient GET failure on the parent service used to create
// (or overwrite) the preview with zero-valued service fields: a public
// Ingress for an internal service, no release hook, legacy mount-all.
func TestDispatch_PROpened_ServiceGetFailureFailsClosed(t *testing.T) {
	t.Parallel()
	d := newDispatcher(t,
		seedProj("alpha", "https://github.com/example/alpha", "main", true, 5),
		seedSvc("alpha", "web"),
	)
	d.Kube.Dynamic.(*dynamicfake.FakeDynamicClient).PrependReactor("get", "kusoservices",
		func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewServiceUnavailable("apiserver blip")
		})

	body := []byte(`{
		"action": "opened",
		"number": 43,
		"pull_request": {
			"head": {"ref": "feat/y", "sha": "abcdef0123456789abcdef0123456789abcdef02"},
			"base": {"ref": "main"},
			"state": "open"
		},
		"repository": {"full_name": "example/alpha"}
	}`)
	_ = d.Dispatch(context.Background(), "pull_request", body)

	if env, err := d.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-pr-43"); !apierrors.IsNotFound(err) {
		t.Fatalf("preview env created without its parent service's fields (env=%v, err=%v)", env, err)
	}
}

type failingPreviewDB struct{}

func (failingPreviewDB) EnsurePRAddons(context.Context, string, int) ([]string, map[string]string, error) {
	return nil, nil, errors.New("provision db-pr-42 for env preview-pr-42: admission webhook timeout")
}

func (failingPreviewDB) DeletePRAddons(context.Context, string, int) error { return nil }

type recordingEmitter struct{ events []builds.EventEnvelope }

func (r *recordingEmitter) Emit(e builds.EventEnvelope) { r.events = append(r.events, e) }

// When the per-PR database clone fails, the preview must not come up on the
// production conn: its seed command and release hook would write to the
// production database on every push. The dispatcher refuses the env and says
// why instead.
func TestDispatch_PROpened_CloneFailureFailsClosed(t *testing.T) {
	t.Parallel()
	d := newDispatcher(t,
		seedProj("alpha", "https://github.com/example/alpha", "main", true, 5),
		seedSvc("alpha", "web"),
	)
	d.AddonConnSecrets = func(context.Context, string) ([]string, error) {
		return []string{"alpha-db-conn"}, nil
	}
	d.PreviewDB = failingPreviewDB{}
	rec := &recordingEmitter{}
	d.Notifier = rec

	body := []byte(`{
		"action": "opened",
		"number": 42,
		"pull_request": {
			"head": {"ref": "feat/x", "sha": "abcdef0123456789abcdef0123456789abcdef01"},
			"base": {"ref": "main"},
			"state": "open"
		},
		"repository": {"full_name": "example/alpha"}
	}`)
	if err := d.Dispatch(context.Background(), "pull_request", body); err != nil {
		t.Fatalf("Dispatch pr: %v", err)
	}

	env, err := d.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-pr-42")
	if !apierrors.IsNotFound(err) {
		t.Fatalf("preview env exists after a failed clone (envFrom=%v, err=%v)", env.Spec.EnvFromSecrets, err)
	}
	if len(rec.events) != 1 {
		t.Fatalf("want one failure event, got %d: %+v", len(rec.events), rec.events)
	}
	ev := rec.events[0]
	if ev.Severity != "error" || ev.Project != "alpha" || ev.Service != "web" || !strings.Contains(ev.Body, "admission webhook timeout") {
		t.Errorf("failure event lacks project/service/cause: %+v", ev)
	}
}
