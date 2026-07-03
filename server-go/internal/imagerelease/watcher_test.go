package imagerelease

import (
	"context"
	"log/slog"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"kuso/server/internal/kube"
	"kuso/server/internal/releaserun"
)

// fakeRunner implements the Runner interface for testing without ever
// touching a real Job.
type fakeRunner struct {
	outcome releaserun.Outcome
	err     error
}

func (f fakeRunner) Run(_ context.Context, _ string, _ *kube.KusoEnvironment, _ *kube.KusoImage) (releaserun.Result, error) {
	return releaserun.Result{Outcome: f.outcome, JobName: "j"}, f.err
}

// fakeKube builds a *kube.Client backed by dynamic/fake, mirroring
// projects_test.go's fakeService helper.
func fakeKube(t *testing.T, seeds ...seed) *kube.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{
		kube.GVRKuso:         "KusoList",
		kube.GVRProjects:     "KusoProjectList",
		kube.GVRServices:     "KusoServiceList",
		kube.GVREnvironments: "KusoEnvironmentList",
		kube.GVRAddons:       "KusoAddonList",
		kube.GVRBuilds:       "KusoBuildList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds)
	for _, s := range seeds {
		if err := dyn.Tracker().Create(s.gvr, s.obj, s.obj.GetNamespace()); err != nil {
			t.Fatalf("seed %s: %v", s.obj.GetName(), err)
		}
	}
	return &kube.Client{Dynamic: dyn}
}

type seed struct {
	gvr schema.GroupVersionResource
	obj *unstructured.Unstructured
}

func seedEnv(name string, spec kube.KusoEnvironmentSpec) seed {
	obj := &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "kuso",
			Labels: map[string]string{
				kube.LabelProject: spec.Project,
				kube.LabelService: spec.Service,
				kube.LabelEnv:     spec.Kind,
			},
		},
		Spec: spec,
	}
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		panic(err)
	}
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   kube.GVREnvironments.Group,
		Version: kube.GVREnvironments.Version,
		Kind:    "KusoEnvironment",
	})
	return seed{gvr: kube.GVREnvironments, obj: u}
}

func TestReconcile_PromotesOnSuccess(t *testing.T) {
	t.Parallel()
	pending := &kube.KusoImage{Repository: "ghcr.io/x/y", Tag: "v2"}
	kc := fakeKube(t, seedEnv("alpha-web-production", kube.KusoEnvironmentSpec{
		Project:      "alpha",
		Service:      "alpha-web",
		Kind:         "production",
		PendingImage: pending,
		Release:      &kube.KusoReleaseSpec{Command: []string{"sh", "-c", "bin/migrate"}},
	}))

	w := &Watcher{
		Kube:      kc,
		Namespace: "kuso",
		Logger:    slog.Default(),
		Release:   fakeRunner{outcome: releaserun.OutcomeSucceeded},
	}

	if err := w.reconcileOnce(context.Background()); err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}

	env, err := kc.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-production")
	if err != nil {
		t.Fatalf("GetKusoEnvironment: %v", err)
	}
	if env.Spec.Image == nil || env.Spec.Image.Repository != pending.Repository || env.Spec.Image.Tag != pending.Tag {
		t.Errorf("expected image promoted to %+v, got %+v", pending, env.Spec.Image)
	}
	if env.Spec.PendingImage != nil {
		t.Errorf("expected pendingImage cleared, got %+v", env.Spec.PendingImage)
	}
}

func TestReconcile_WithholdsOnFailure(t *testing.T) {
	t.Parallel()
	pending := &kube.KusoImage{Repository: "ghcr.io/x/y", Tag: "v2"}
	kc := fakeKube(t, seedEnv("alpha-web-production", kube.KusoEnvironmentSpec{
		Project:      "alpha",
		Service:      "alpha-web",
		Kind:         "production",
		PendingImage: pending,
		Release:      &kube.KusoReleaseSpec{Command: []string{"sh", "-c", "bin/migrate"}},
	}))

	var notified []string
	w := &Watcher{
		Kube:      kc,
		Namespace: "kuso",
		Logger:    slog.Default(),
		Release:   fakeRunner{outcome: releaserun.OutcomeFailed},
		Notify: func(project, service, msg string) {
			notified = append(notified, project+"/"+service+": "+msg)
		},
	}

	if err := w.reconcileOnce(context.Background()); err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}

	env, err := kc.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-production")
	if err != nil {
		t.Fatalf("GetKusoEnvironment: %v", err)
	}
	if env.Spec.Image != nil {
		t.Errorf("expected image to stay nil on failure, got %+v", env.Spec.Image)
	}
	if env.Spec.PendingImage == nil || env.Spec.PendingImage.Tag != pending.Tag {
		t.Errorf("expected pendingImage retained, got %+v", env.Spec.PendingImage)
	}
	if len(notified) != 1 {
		t.Errorf("expected exactly one Notify call, got %+v", notified)
	}
}
