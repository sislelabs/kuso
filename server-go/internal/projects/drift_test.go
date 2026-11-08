package projects

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// helm-operator lists Initialized before Deployed; a first-match read
// reported every healthy release as Pending.
func TestExtractHelmStatus_DeployedWinsOverInitialized(t *testing.T) {
	t.Parallel()
	status := map[string]any{"conditions": []any{
		map[string]any{"type": "Initialized", "status": "True"},
		map[string]any{"type": "Deployed", "status": "True", "reason": "UpgradeSuccessful"},
	}}
	if phase, _ := extractHelmStatus(status); phase != "Deployed" {
		t.Errorf("phase = %q, want Deployed", phase)
	}
	onlyInit := map[string]any{"conditions": []any{
		map[string]any{"type": "Initialized", "status": "True"},
	}}
	if phase, _ := extractHelmStatus(onlyInit); phase != "Pending" {
		t.Errorf("initialized-only phase = %q, want Pending", phase)
	}
	failed := map[string]any{"conditions": []any{
		map[string]any{"type": "Initialized", "status": "True"},
		map[string]any{"type": "Deployed", "status": "True"},
		map[string]any{"type": "ReleaseFailed", "status": "True", "message": "boom"},
	}}
	if phase, msg := extractHelmStatus(failed); phase != "Failed" || msg != "boom" {
		t.Errorf("failed = %q/%q, want Failed/boom", phase, msg)
	}
}

func envWithManagedTimes(kusoServer, helmOperator time.Time) kube.KusoEnvironment {
	return kube.KusoEnvironment{ObjectMeta: metav1.ObjectMeta{ManagedFields: []metav1.ManagedFieldsEntry{
		{Manager: "helm-operator", Time: &metav1.Time{Time: helmOperator}},
		{Manager: "kuso-server", Time: &metav1.Time{Time: kusoServer}},
	}}}
}

// A spec edit that renders an identical release (sleep-only) gets no
// helm-operator write, which used to pin rolloutPending=true forever.
func TestReconcilePending_ExpiresForNoOpEdits(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 8, 20, 0, 0, 0, time.UTC)
	reconciled := now.Add(-30 * time.Minute)
	if !reconcilePending(envWithManagedTimes(now.Add(-10*time.Second), reconciled), now) {
		t.Error("fresh edit after the last reconcile should be pending")
	}
	if reconcilePending(envWithManagedTimes(now.Add(-10*time.Minute), reconciled), now) {
		t.Error("edit 10m ago with no operator write must not stay pending")
	}
	if reconcilePending(envWithManagedTimes(now.Add(-10*time.Second), now.Add(-5*time.Second)), now) {
		t.Error("edit already reconciled must not be pending")
	}
}

// A worker saved without a port gets the chart default (8080) on its env;
// drift reported a permanent "port" change nothing could reconcile.
func TestGetDrift_PortlessWorkerIsNotPending(t *testing.T) {
	t.Parallel()
	env := typedSeed(kube.GVREnvironments, "KusoEnvironment", "alpha-jobs-production",
		&kube.KusoEnvironment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "alpha-jobs-production",
				Namespace: "kuso",
				Labels:    map[string]string{labelProject: "alpha", labelService: "jobs", labelEnv: "production"},
			},
			Spec: kube.KusoEnvironmentSpec{
				Project: "alpha", Service: serviceCRName("alpha", "jobs"), Kind: "production",
				Branch: "main", Port: 8080, Runtime: "worker", SharedEnvKeys: []string{},
			},
		})
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "jobs", kube.KusoServiceSpec{
			Project: "alpha", Runtime: "worker", FromService: "api", SharedEnvKeys: []string{},
		}),
		env,
	)
	d, err := s.GetDrift(context.Background(), "alpha", "jobs")
	if err != nil {
		t.Fatalf("GetDrift: %v", err)
	}
	for _, f := range d.SpecPending {
		if f == "port" {
			t.Fatalf("SpecPending = %v, port must not drift for a portless worker", d.SpecPending)
		}
	}
}
