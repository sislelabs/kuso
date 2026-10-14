package addons

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"kuso/server/internal/kube"
)

// rawRetention reads spec.backup.retentionDays off the stored object, i.e.
// what the CRD defaulting and the chart actually see.
func rawRetention(t *testing.T, s *Service) (int64, bool) {
	t.Helper()
	u, err := s.Kube.Dynamic.Resource(kube.GVRAddons).Namespace("kuso").Get(context.Background(), "alpha-pg", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	v, found, err := unstructured.NestedFieldNoCopy(u.Object, "spec", "backup", "retentionDays")
	if err != nil || !found {
		return 0, false
	}
	switch n := v.(type) {
	case int64:
		return n, true
	case float64:
		return int64(n), true
	}
	t.Fatalf("retentionDays has type %T", v)
	return 0, false
}

// --retention 0 must reach the CR as an explicit 0; dropping it lets the
// CRD default 14 apply and the prune deletes "keep forever" dumps.
func TestUpdate_RetentionZeroPersistsAsKeepForever(t *testing.T) {
	s := fakeService(t,
		seedProj("alpha"),
		typedSeed(kube.GVRAddons, "KusoAddon", &kube.KusoAddon{
			ObjectMeta: metav1.ObjectMeta{Name: "alpha-pg", Namespace: "kuso"},
			Spec: kube.KusoAddonSpec{
				Project: "alpha",
				Kind:    "postgres",
				Backup:  &kube.KusoBackup{Schedule: "0 3 * * *", RetentionDays: 14},
			},
		}),
	)
	zero := 0
	if _, err := s.Update(context.Background(), "alpha", "pg", UpdateAddonRequest{
		Backup: &UpdateBackupPatch{RetentionDays: &zero},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, found := rawRetention(t, s)
	if !found || got != 0 {
		t.Fatalf("stored retentionDays = %d (found=%v), want explicit 0", got, found)
	}
}

// Setting only a schedule on an addon with no backup block must keep the
// 14-day default, not turn into keep-forever via Go's zero value.
func TestUpdate_ScheduleOnlyKeepsDefaultRetention(t *testing.T) {
	s := fakeService(t, seedProj("alpha"), seedAddon("alpha", "pg", "postgres"))
	sched := "0 3 * * *"
	if _, err := s.Update(context.Background(), "alpha", "pg", UpdateAddonRequest{
		Backup: &UpdateBackupPatch{Schedule: &sched},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got, found := rawRetention(t, s); !found || got != DefaultBackupRetentionDays {
		t.Fatalf("stored retentionDays = %d (found=%v), want %d", got, found, DefaultBackupRetentionDays)
	}
}
