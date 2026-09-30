package addons

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

func addonPVC(name, instance string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: "kuso",
		Labels: map[string]string{
			"app.kubernetes.io/name":     "kusoaddon",
			"app.kubernetes.io/instance": instance,
		},
	}}
}

// Deleting a native addon keeps its data PVC. Re-adding the same name used
// to silently mount that old data — the "recreate at the new version"
// advice then crash-looped the new engine against the old data directory.
func TestAdd_RefusesWhenRetainedDataPVCExists(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProj("alpha"))
	s.Kube.Clientset = kubefake.NewSimpleClientset(addonPVC("data-alpha-pg-0", "alpha-pg"))

	_, err := s.Add(context.Background(), "alpha", CreateAddonRequest{Name: "pg", Kind: "postgres"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("got %v, want ErrConflict", err)
	}
	if !strings.Contains(err.Error(), "data from a previous pg still exists") {
		t.Errorf("message = %q", err.Error())
	}
}

// A PR preview clone must never inherit an earlier PR's data, and refusing
// would leave the preview without a database. Its leftovers are stale by
// definition, so the create purges them and proceeds.
func TestAdd_PreviewClonePurgesLeftoverData(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProj("alpha"))
	stale := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "alpha-pg-pr-7-conn", Namespace: "kuso"}}
	s.Kube.Clientset = kubefake.NewSimpleClientset(addonPVC("data-alpha-pg-pr-7-0", "alpha-pg-pr-7"), stale)

	_, err := s.Add(context.Background(), "alpha", CreateAddonRequest{
		Name: "pg-pr-7", Kind: "postgres",
		ExtraLabels: map[string]string{"kuso.sislelabs.com/preview-pr": "7"},
	})
	if err != nil {
		t.Fatalf("Add preview clone: %v", err)
	}
	cs := s.Kube.Clientset.CoreV1()
	if _, err := cs.PersistentVolumeClaims("kuso").Get(context.Background(), "data-alpha-pg-pr-7-0", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("stale preview PVC survived: err=%v", err)
	}
	if _, err := cs.Secrets("kuso").Get(context.Background(), "alpha-pg-pr-7-conn", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("stale preview conn secret survived: err=%v", err)
	}
}

// A different addon's leftover data must not block an unrelated name.
func TestAdd_OtherAddonsPVCDoesNotBlock(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProj("alpha"))
	s.Kube.Clientset = kubefake.NewSimpleClientset(addonPVC("data-alpha-pg-0", "alpha-pg"))

	if _, err := s.Add(context.Background(), "alpha", CreateAddonRequest{Name: "pg2", Kind: "postgres"}); err != nil {
		t.Fatalf("Add pg2: %v", err)
	}
}

// A PVC already being deleted (preview teardown racing a re-create) is on
// its way out and must not block.
func TestAdd_TerminatingPVCDoesNotBlock(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProj("alpha"))
	p := addonPVC("data-alpha-pg-0", "alpha-pg")
	now := metav1.Now()
	p.DeletionTimestamp = &now
	p.Finalizers = []string{"kubernetes.io/pvc-protection"}
	s.Kube.Clientset = kubefake.NewSimpleClientset(p)

	if _, err := s.Add(context.Background(), "alpha", CreateAddonRequest{Name: "pg", Kind: "postgres"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
}

func seedPlainAddon(project, short string) seed {
	return typedSeed(kube.GVRAddons, "KusoAddon", &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{
			Name: project + "-" + short, Namespace: "kuso",
			Labels: map[string]string{"kuso.sislelabs.com/project": project},
		},
		Spec: kube.KusoAddonSpec{Project: project, Kind: "postgres"},
	})
}

func connSecret(name string) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso"}}
}

func TestDelete_DefaultKeepsDataAndConn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := fakeService(t, seedProj("alpha"), seedPlainAddon("alpha", "pg"))
	cs := kubefake.NewSimpleClientset(addonPVC("data-alpha-pg-0", "alpha-pg"), connSecret("alpha-pg-conn"))
	s.Kube.Clientset = cs

	if err := s.Delete(ctx, "alpha", "pg"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := cs.CoreV1().PersistentVolumeClaims("kuso").Get(ctx, "data-alpha-pg-0", metav1.GetOptions{}); err != nil {
		t.Errorf("PVC should be retained: %v", err)
	}
	if _, err := cs.CoreV1().Secrets("kuso").Get(ctx, "alpha-pg-conn", metav1.GetOptions{}); err != nil {
		t.Errorf("conn secret should be retained: %v", err)
	}
}

func TestDeleteWith_PurgeDataRemovesPVCAndConn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := fakeService(t, seedProj("alpha"), seedPlainAddon("alpha", "pg"))
	cs := kubefake.NewSimpleClientset(
		addonPVC("data-alpha-pg-0", "alpha-pg"),
		addonPVC("data-alpha-pg2-0", "alpha-pg2"),
		connSecret("alpha-pg-conn"),
	)
	s.Kube.Clientset = cs

	if err := s.DeleteWith(ctx, "alpha", "pg", DeleteOptions{PurgeData: true}); err != nil {
		t.Fatalf("DeleteWith: %v", err)
	}
	if _, err := cs.CoreV1().PersistentVolumeClaims("kuso").Get(ctx, "data-alpha-pg-0", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("PVC should be purged, get err = %v", err)
	}
	if _, err := cs.CoreV1().Secrets("kuso").Get(ctx, "alpha-pg-conn", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("conn secret should be purged, get err = %v", err)
	}
	if _, err := cs.CoreV1().PersistentVolumeClaims("kuso").Get(ctx, "data-alpha-pg2-0", metav1.GetOptions{}); err != nil {
		t.Errorf("another addon's PVC was touched: %v", err)
	}
	// And the name is reusable afterwards.
	if _, err := s.Add(ctx, "alpha", CreateAddonRequest{Name: "pg", Kind: "postgres"}); err != nil {
		t.Errorf("re-Add after purge: %v", err)
	}
}

func TestUpdate_VersionErrorPointsAtNewName(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProj("alpha"), typedSeed(kube.GVRAddons, "KusoAddon", &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-pg", Namespace: "kuso",
			Labels: map[string]string{"kuso.sislelabs.com/project": "alpha"}},
		Spec: kube.KusoAddonSpec{Project: "alpha", Kind: "postgres", Version: "16"},
	}))
	v := "17"
	_, err := s.Update(context.Background(), "alpha", "pg", UpdateAddonRequest{Version: &v})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("got %v, want ErrConflict", err)
	}
	if !strings.Contains(err.Error(), "recreate under a new name, then restore") {
		t.Errorf("message = %q", err.Error())
	}
}
