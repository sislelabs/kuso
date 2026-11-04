package handlers

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// DATA-06: an instance-shared tenant's database is dumped by the instance
// addon's CronJob under its prefix as <ts>.db-<tenant db>.sql.gz. The tenant's
// own Backups/Restore must resolve exactly those keys.
func TestInstanceTenantBackups_ResolvesBackingPrefixAndDB(t *testing.T) {
	t.Parallel()
	backing := &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{Name: "kuso-instance-pg", Namespace: "kuso",
			Labels: map[string]string{kube.LabelProject: "kuso-instance"}},
		Spec: kube.KusoAddonSpec{Project: "kuso-instance", Kind: "postgres",
			Backup: &kube.KusoBackup{Schedule: "0 3 * * *", Bucket: "instance-bucket"}},
	}
	tenant := &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{Name: "shop-db"},
		Spec:       kube.KusoAddonSpec{Project: "shop", Kind: "postgres", UseInstanceAddon: "pg"},
	}
	h := backupsHandlerWithAddons(t, backing)
	h.Kube.Clientset = fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "shop-db-conn", Namespace: "kuso-shop"},
		Data:       map[string][]byte{"POSTGRES_DB": []byte("shop_db")},
	})
	loc, err := h.instanceTenantBackups(context.Background(), "kuso-shop", tenant)
	if err != nil {
		t.Fatalf("instanceTenantBackups: %v", err)
	}
	if loc == nil || loc.prefix != "kuso-instance/kuso-instance-pg/" || loc.db != "shop_db" || loc.bucket != "instance-bucket" {
		t.Fatalf("got %+v", loc)
	}
	for key, want := range map[string]bool{
		"kuso-instance/kuso-instance-pg/20261101T030000Z.db-shop_db.sql.gz":     true,
		"kuso-instance/kuso-instance-pg/20261101T030000Z.db-other_db.sql.gz":    false, // another tenant
		"kuso-instance/kuso-instance-pg/20261101T030000Z.sql.gz":                false, // the instance's own DB
		"kuso-instance/kuso-instance-pg/x/20261101T030000Z.db-shop_db.sql.gz":   false,
		"shop/shop-db/20261101T030000Z.db-shop_db.sql.gz":                       false,
		"kuso-instance/kuso-instance-pg/20261101T030000Z.db-shop_db.sql.gz.bak": false,
	} {
		if got := loc.owns(key); got != want {
			t.Errorf("owns(%q) = %v, want %v", key, got, want)
		}
	}
}

// No backing instance addon (an external instance server): no backups, not
// an error.
func TestInstanceTenantBackups_NoBackingAddon(t *testing.T) {
	t.Parallel()
	h := backupsHandlerWithAddons(t)
	h.Kube.Clientset = fake.NewSimpleClientset()
	loc, err := h.instanceTenantBackups(context.Background(), "kuso-shop", &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{Name: "shop-db"},
		Spec:       kube.KusoAddonSpec{Project: "shop", Kind: "postgres", UseInstanceAddon: "pg"},
	})
	if err != nil || loc != nil {
		t.Fatalf("got %+v, %v; want nil, nil", loc, err)
	}
}
