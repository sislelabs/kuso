package vulnscan

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/db"
)

// A gate scan must not wait behind a nightly sweep scan holding the one
// running slot (SCL-11): the nightly scan is requeued and the gate scan
// starts on the next step.
func TestWorker_GateScanPreemptsNightly(t *testing.T) {
	ctx := context.Background()
	store := &memStore{scans: []db.VulnScan{
		{ID: 1, Status: db.VulnStatusQueued, Reason: db.VulnReasonNightly, ImageRef: "postgres:16"},
	}}
	w := &Worker{Kube: fake.NewClientset(), Namespace: "kuso", Store: store}
	if err := w.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if store.scans[0].Status != db.VulnStatusRunning || store.scans[0].JobName == "" {
		t.Fatalf("nightly scan not started: %+v", store.scans[0])
	}

	store.scans = append(store.scans, db.VulnScan{ID: 2, Status: db.VulnStatusQueued, Reason: db.VulnReasonGate, ImageRef: "shop/web:1"})
	if err := w.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if store.scans[0].Status != db.VulnStatusQueued {
		t.Fatalf("nightly scan not requeued: %+v", store.scans[0])
	}
	if _, err := w.Kube.BatchV1().Jobs("kuso").Get(ctx, "kuso-vulnscan-1", metav1.GetOptions{}); err == nil {
		t.Error("preempted scan's Job still exists")
	}
	// The real queue orders gate scans first; the mem store claims in
	// slice order, so drop the requeued row to model that.
	store.scans = store.scans[1:]
	if err := w.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if store.scans[0].Status != db.VulnStatusRunning {
		t.Fatalf("gate scan not started after preemption: %+v", store.scans[0])
	}
}

// A manual scan is never preempted.
func TestWorker_GateScanDoesNotPreemptManual(t *testing.T) {
	ctx := context.Background()
	store := &memStore{scans: []db.VulnScan{
		{ID: 1, Status: db.VulnStatusQueued, Reason: db.VulnReasonManual, ImageRef: "postgres:16"},
	}}
	w := &Worker{Kube: fake.NewClientset(), Namespace: "kuso", Store: store}
	_ = w.Step(ctx)
	store.scans = append(store.scans, db.VulnScan{ID: 2, Status: db.VulnStatusQueued, Reason: db.VulnReasonGate, ImageRef: "shop/web:1"})
	_ = w.Step(ctx)
	if store.scans[0].Status != db.VulnStatusRunning {
		t.Fatalf("manual scan was preempted: %+v", store.scans[0])
	}
}
