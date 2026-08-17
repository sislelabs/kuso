package projects

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuso/server/internal/kube"
)

// These are the regression tests for the delete-cascade error-swallowing
// class that caused the v0.21.6 production preview-PVC leak: `_ =` on
// child deletes meant a failed PVC/secret delete looked like a clean
// teardown, and the orphan was only found months later by hand. The
// contract pinned here:
//
//   - NotFound stays ignored (deletes are idempotent/resumable),
//   - any OTHER child-delete failure is COLLECTED, not swallowed,
//   - the cascade KEEPS GOING past the failure (don't strand the
//     remaining children just because one delete broke),
//   - the function returns a joined error so callers/audit see it.

func newCascadeFixture(t *testing.T, seeds []seed, objs ...runtime.Object) (*Service, *dynamicfake.FakeDynamicClient, *kubefake.Clientset) {
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
	for _, sd := range seeds {
		if err := dyn.Tracker().Create(sd.gvr, sd.obj, sd.obj.GetNamespace()); err != nil {
			t.Fatalf("seed %s: %v", sd.obj.GetName(), err)
		}
	}
	cs := kubefake.NewSimpleClientset(objs...)
	return New(&kube.Client{Dynamic: dyn, Clientset: cs}, "kuso"), dyn, cs
}

// TestDeleteEnvironment_PVCDeleteFailureSurfacesAndCascadeContinues:
// deleting a named env whose addon-clone PVC delete fails must (a) still
// delete everything downstream of the failure — here the env's TLS
// Secret — and (b) return an error naming the orphaned PVC, instead of
// the historical `_ =` false success.
func TestDeleteEnvironment_PVCDeleteFailureSurfacesAndCascadeContinues(t *testing.T) {
	t.Parallel()
	const ns = "kuso"

	cloneAddon := typedSeed(kube.GVRAddons, "KusoAddon", "alpha-pg-staging", &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "alpha-pg-staging",
			Namespace: ns,
			Labels:    map[string]string{labelProject: "alpha", labelEnv: "staging"},
		},
		Spec: kube.KusoAddonSpec{Project: "alpha", Kind: "postgres"},
	})
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
		Name:      "data-alpha-pg-staging-0",
		Namespace: ns,
		Labels:    map[string]string{"app.kubernetes.io/instance": "alpha-pg-staging"},
	}}
	tlsSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-staging-tls", Namespace: ns}, Type: corev1.SecretTypeTLS}

	s, dyn, cs := newCascadeFixture(t, []seed{
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedEnv("alpha", "web", "staging", "main", "alpha-web-staging"),
		cloneAddon,
	}, pvc, tlsSecret)

	// The PVC delete blows up (RBAC change, finalizer webhook down, ...).
	cs.PrependReactor("delete", "persistentvolumeclaims", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("injected pvc delete failure")
	})

	err := s.DeleteEnvironment(context.Background(), "alpha", "alpha-web-staging")
	if err == nil {
		t.Fatal("DeleteEnvironment returned nil despite a failed PVC delete — the orphan is invisible (the v0.21.6 leak class)")
	}
	if !strings.Contains(err.Error(), "injected pvc delete failure") {
		t.Errorf("error must carry the underlying delete failure, got: %v", err)
	}
	if !strings.Contains(err.Error(), "data-alpha-pg-staging-0") {
		t.Errorf("error must name the orphaned PVC so an operator can find it, got: %v", err)
	}

	// Cascade continued past the failure: TLS secret and CRs are gone.
	if _, gerr := cs.CoreV1().Secrets(ns).Get(context.Background(), "alpha-web-staging-tls", metav1.GetOptions{}); !apierrors.IsNotFound(gerr) {
		t.Errorf("TLS secret must still be deleted after the PVC failure (cascade must not abort), err=%v", gerr)
	}
	if _, gerr := dyn.Resource(kube.GVREnvironments).Namespace(ns).Get(context.Background(), "alpha-web-staging", metav1.GetOptions{}); !apierrors.IsNotFound(gerr) {
		t.Errorf("env CR must be deleted, err=%v", gerr)
	}
	if _, gerr := dyn.Resource(kube.GVRAddons).Namespace(ns).Get(context.Background(), "alpha-pg-staging", metav1.GetOptions{}); !apierrors.IsNotFound(gerr) {
		t.Errorf("clone addon CR must be deleted, err=%v", gerr)
	}
}

// TestDeleteEnvironment_NotFoundChildrenStayIgnored pins the idempotency
// half of the contract: a resumed delete where the children are already
// gone must stay a clean success (NotFound is not an error).
func TestDeleteEnvironment_NotFoundChildrenStayIgnored(t *testing.T) {
	t.Parallel()
	s, _, _ := newCascadeFixture(t, []seed{
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedEnv("alpha", "web", "staging", "main", "alpha-web-staging"),
	})
	if err := s.DeleteEnvironment(context.Background(), "alpha", "alpha-web-staging"); err != nil {
		t.Fatalf("delete with no children must succeed (NotFound tolerated), got: %v", err)
	}
}

// TestDeleteProject_TLSSecretDeleteFailureSurfacesAndCascadeFinishes:
// project delete used to `_ =` the per-env cert-manager TLS Secret
// deletes. A failure there must not abort the cascade (the project CR
// and every other child must still go) but must surface in the returned
// error so the operator knows a Secret orphaned in the shared namespace.
func TestDeleteProject_TLSSecretDeleteFailureSurfacesAndCascadeFinishes(t *testing.T) {
	t.Parallel()
	const ns = "kuso"

	tlsSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-production-tls", Namespace: ns}, Type: corev1.SecretTypeTLS}
	s, dyn, cs := newCascadeFixture(t, []seed{
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Port: 8080}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	}, tlsSecret)

	cs.PrependReactor("delete", "secrets", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if da, ok := a.(k8stesting.DeleteActionImpl); ok && da.GetName() == "alpha-web-production-tls" {
			return true, nil, errors.New("injected tls delete failure")
		}
		return false, nil, nil
	})

	err := s.Delete(context.Background(), "alpha")
	if err == nil {
		t.Fatal("project delete returned nil despite a failed TLS-secret delete — orphan invisible")
	}
	if !strings.Contains(err.Error(), "injected tls delete failure") || !strings.Contains(err.Error(), "alpha-web-production-tls") {
		t.Errorf("error must name the orphaned TLS secret + cause, got: %v", err)
	}

	// The cascade must have finished: project + service CRs gone.
	if _, gerr := dyn.Resource(kube.GVRProjects).Namespace(ns).Get(context.Background(), "alpha", metav1.GetOptions{}); !apierrors.IsNotFound(gerr) {
		t.Errorf("project CR must be deleted despite TLS cleanup failure, err=%v", gerr)
	}
	if _, gerr := dyn.Resource(kube.GVRServices).Namespace(ns).Get(context.Background(), "alpha-web", metav1.GetOptions{}); !apierrors.IsNotFound(gerr) {
		t.Errorf("service CR must be deleted, err=%v", gerr)
	}
}

// TestDeleteEnvGroup_SurfacesCleanupInstanceAddonFailure: the instance-
// shared addon's DB/role/conn-secret cleanup ran with `_ =`. A failure
// there orphans a live credential + DB on the shared server; it must be
// surfaced while the CR delete still proceeds (the group is going away
// either way — but the operator has to learn about the orphan).
func TestDeleteEnvGroup_SurfacesCleanupInstanceAddonFailure(t *testing.T) {
	t.Parallel()
	cloned := typedSeed(kube.GVRAddons, "KusoAddon", "acme-db-staging", &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "acme-db-staging",
			Namespace: "kuso",
			Labels:    map[string]string{labelProject: "acme", labelEnv: "staging"},
		},
		Spec: kube.KusoAddonSpec{Project: "acme", Kind: "postgres", UseInstanceAddon: "pg"},
	})
	s := fakeService(t,
		seedProject("acme", kube.KusoProjectSpec{BaseDomain: "apps.example.com"}),
		cloned,
	)
	s.CleanupInstanceAddon = func(ctx context.Context, project, addonShort string) error {
		return errors.New("injected instance cleanup failure")
	}

	err := s.DeleteEnvGroup(context.Background(), "acme", "staging")
	if err == nil {
		t.Fatal("DeleteEnvGroup returned nil despite a failed instance-addon cleanup — orphaned DB/role invisible")
	}
	if !strings.Contains(err.Error(), "injected instance cleanup failure") || !strings.Contains(err.Error(), "db-staging") {
		t.Errorf("error must name the addon + cause, got: %v", err)
	}
	// The addon CR itself must still have been deleted (cascade continued).
	if _, gerr := s.Kube.GetKusoAddon(context.Background(), "kuso", "acme-db-staging"); !apierrors.IsNotFound(gerr) {
		t.Errorf("addon CR must be deleted despite cleanup failure, err=%v", gerr)
	}
}

// TestCreateEnvGroup_RollbackFailureIsSurfacedNotSwallowed: when the
// clone aborts mid-way and the best-effort rollback ALSO fails, the
// caller used to see only the original cause — the half-rolled-back
// orphans (here a cloned addon CR that refused to delete) were silent.
// Both must appear in the returned error.
func TestCreateEnvGroup_RollbackFailureIsSurfacedNotSwallowed(t *testing.T) {
	t.Parallel()
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
	seeds := []seed{
		seedProject("acme", kube.KusoProjectSpec{BaseDomain: "apps.example.com"}),
		seedService("acme", "web", kube.KusoServiceSpec{Project: "acme", Port: 8080}),
		seedEnv("acme", "web", "production", "main", "acme-web-production"),
		typedSeed(kube.GVRAddons, "KusoAddon", "acme-db", &kube.KusoAddon{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "acme-db",
				Namespace: "kuso",
				Labels:    map[string]string{labelProject: "acme"},
			},
			Spec: kube.KusoAddonSpec{Project: "acme", Kind: "postgres"},
		}),
		// Decoy that collides with the "web" clone target so the create
		// fails AFTER the addon clone landed (same shape as the
		// provisioned-instance-addon rollback test).
		typedSeed(kube.GVRServices, "KusoService", "acme-web-staging", &kube.KusoService{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "acme-web-staging",
				Namespace: "kuso",
				Labels: map[string]string{
					labelProject: "acme",
					labelService: "web-staging",
					labelEnv:     "other",
				},
			},
			Spec: kube.KusoServiceSpec{Project: "acme", Port: 8080},
		}),
	}
	for _, sd := range seeds {
		if err := dyn.Tracker().Create(sd.gvr, sd.obj, sd.obj.GetNamespace()); err != nil {
			t.Fatalf("seed %s: %v", sd.obj.GetName(), err)
		}
	}
	// Rollback's addon delete fails — the cloned addon CR is now an orphan.
	dyn.PrependReactor("delete", "kusoaddons", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("injected rollback delete failure")
	})
	s := New(&kube.Client{Dynamic: dyn}, "kuso")

	_, err := s.CreateEnvGroup(context.Background(), "acme", CreateEnvGroupRequest{Name: "staging"})
	if err == nil {
		t.Fatal("CreateEnvGroup succeeded, want failure from service-clone collision")
	}
	if !strings.Contains(err.Error(), "clone service web") {
		t.Errorf("original cause must be preserved, got: %v", err)
	}
	if !strings.Contains(err.Error(), "injected rollback delete failure") {
		t.Errorf("rollback failure (orphaned clone addon) must be surfaced alongside the cause, got: %v", err)
	}
}
