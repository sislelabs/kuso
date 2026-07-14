package addons

// add_guards_test.go covers the Add-path guards introduced by the
// platform review: the kind/HA support matrix (mirrors the kusoaddon
// chart's unsupported.yaml), the concurrent-create → conflict mapping,
// and the side-effect cleanup when the CR create fails after durable
// side effects (mirrored conn secret) were provisioned.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"kuso/server/internal/kube"
)

// TestAdd_HAUnsupportedKindRejected mirrors the chart guard in
// operator/helm-charts/kusoaddon/templates/unsupported.yaml: kinds with
// no -ha template must be refused at the API boundary, before a CR that
// can never render is written.
func TestAdd_HAUnsupportedKindRejected(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProj("alpha"))
	for _, kind := range []string{"valkey", "mongodb", "rabbitmq", "redpanda"} {
		_, err := s.Add(context.Background(), "alpha", CreateAddonRequest{Name: "x", Kind: kind, HA: true})
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("kind %s ha=true: got %v, want ErrInvalid", kind, err)
		}
	}
}

// TestAdd_HASupportedKindsPass — the HA-capable kinds (real -ha chart
// templates) and the non-HA form of a guarded kind must still create.
func TestAdd_HASupportedKindsPass(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProj("alpha"))
	for i, kind := range []string{"postgres", "redis", "nats"} {
		name := fmt.Sprintf("ha%d", i)
		if _, err := s.Add(context.Background(), "alpha", CreateAddonRequest{Name: name, Kind: kind, HA: true}); err != nil {
			t.Errorf("kind %s ha=true: %v", kind, err)
		}
	}
	if _, err := s.Add(context.Background(), "alpha", CreateAddonRequest{Name: "kv", Kind: "valkey"}); err != nil {
		t.Errorf("valkey ha=false: %v", err)
	}
}

// TestAdd_ConcurrentCreateMapsToConflict — a create that loses the race
// between the existence preflight and the CR write must surface the
// same ErrConflict + "already exists" message the preflight produces,
// not an opaque 500.
func TestAdd_ConcurrentCreateMapsToConflict(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProj("alpha"))
	dyn := s.Kube.Dynamic.(*dynamicfake.FakeDynamicClient)
	dyn.PrependReactor("create", "kusoaddons", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewAlreadyExists(kube.GVRAddons.GroupResource(), "alpha-pg")
	})
	_, err := s.Add(context.Background(), "alpha", CreateAddonRequest{Name: "pg", Kind: "postgres"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("got %v, want ErrConflict", err)
	}
	if !strings.Contains(err.Error(), "addon alpha/pg already exists") {
		t.Errorf("conflict message = %q, want the preflight's 'already exists' shape", err.Error())
	}
}

// TestAdd_CleanupOnCreateFailure — Add mirrors the external conn secret
// BEFORE the CR write. When the CR create then fails (non-conflict),
// the mirrored secret must not linger with no addon owning it.
func TestAdd_CleanupOnCreateFailure(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProj("alpha"))
	cs := kubefake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "user-db-creds", Namespace: "kuso"},
		Data:       map[string][]byte{"DATABASE_URL": []byte("postgres://u:p@h/db")},
	})
	s.Kube.Clientset = cs
	dyn := s.Kube.Dynamic.(*dynamicfake.FakeDynamicClient)
	dyn.PrependReactor("create", "kusoaddons", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("apiserver hiccup")
	})

	_, err := s.Add(context.Background(), "alpha", CreateAddonRequest{
		Name: "pg", Kind: "postgres",
		External: &kube.KusoAddonExternal{SecretName: "user-db-creds"},
	})
	if err == nil {
		t.Fatal("Add succeeded, want create failure")
	}
	if errors.Is(err, ErrConflict) {
		t.Fatalf("got conflict, want plain failure: %v", err)
	}
	if _, gerr := cs.CoreV1().Secrets("kuso").Get(context.Background(), "alpha-pg-conn", metav1.GetOptions{}); !apierrors.IsNotFound(gerr) {
		t.Errorf("mirrored conn secret survived the failed create (get err = %v), want NotFound", gerr)
	}
	// The user's SOURCE secret must be untouched.
	if _, gerr := cs.CoreV1().Secrets("kuso").Get(context.Background(), "user-db-creds", metav1.GetOptions{}); gerr != nil {
		t.Errorf("source secret: %v", gerr)
	}
}

// TestAdd_CommittedDespiteErrorLeavesSideEffects — a create can return
// an error to the client (e.g. a client-side context deadline or a
// transport blip) AFTER the apiserver already persisted the CR. In that
// case the side effects (mirrored conn secret / instance DB) belong to a
// LIVE addon and must NOT be destroyed. Cleanup re-GETs the CR and skips
// when it's present. Here the create reactor errors but ALSO leaves the
// object in the tracker, so the re-GET finds it.
func TestAdd_CommittedDespiteErrorLeavesSideEffects(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProj("alpha"))
	cs := kubefake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "user-db-creds", Namespace: "kuso"},
		Data:       map[string][]byte{"DATABASE_URL": []byte("postgres://u:p@h/db")},
	})
	s.Kube.Clientset = cs
	dyn := s.Kube.Dynamic.(*dynamicfake.FakeDynamicClient)
	dyn.PrependReactor("create", "kusoaddons", func(action ktesting.Action) (bool, runtime.Object, error) {
		// Persist the object into the tracker (the apiserver committed it),
		// then return an error to the caller (the client never saw success).
		ca := action.(ktesting.CreateAction)
		_ = dyn.Tracker().Create(kube.GVRAddons, ca.GetObject(), "kuso")
		return true, nil, fmt.Errorf("connection reset by peer")
	})

	_, err := s.Add(context.Background(), "alpha", CreateAddonRequest{
		Name: "pg", Kind: "postgres",
		External: &kube.KusoAddonExternal{SecretName: "user-db-creds"},
	})
	if err == nil {
		t.Fatal("Add succeeded, want the create error surfaced")
	}
	if errors.Is(err, ErrConflict) {
		t.Fatalf("got conflict, want plain failure: %v", err)
	}
	// The committed CR's conn secret must SURVIVE — destroying it would
	// corrupt a live addon.
	if _, gerr := cs.CoreV1().Secrets("kuso").Get(context.Background(), "alpha-pg-conn", metav1.GetOptions{}); gerr != nil {
		t.Errorf("conn secret was cleaned up despite the CR being committed (get err = %v)", gerr)
	}
}

// TestAdd_ConflictLeavesSideEffectsForWinner — when the create loses to
// a CONCURRENT create of the same name, the conn secret now serves the
// winner's CR and must NOT be cleaned up.
func TestAdd_ConflictLeavesSideEffectsForWinner(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProj("alpha"))
	cs := kubefake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "user-db-creds", Namespace: "kuso"},
		Data:       map[string][]byte{"DATABASE_URL": []byte("postgres://u:p@h/db")},
	})
	s.Kube.Clientset = cs
	dyn := s.Kube.Dynamic.(*dynamicfake.FakeDynamicClient)
	dyn.PrependReactor("create", "kusoaddons", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewAlreadyExists(kube.GVRAddons.GroupResource(), "alpha-pg")
	})

	_, err := s.Add(context.Background(), "alpha", CreateAddonRequest{
		Name: "pg", Kind: "postgres",
		External: &kube.KusoAddonExternal{SecretName: "user-db-creds"},
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("got %v, want ErrConflict", err)
	}
	if _, gerr := cs.CoreV1().Secrets("kuso").Get(context.Background(), "alpha-pg-conn", metav1.GetOptions{}); gerr != nil {
		t.Errorf("conn secret was cleaned up on conflict (get err = %v); the winning create needs it", gerr)
	}
}
