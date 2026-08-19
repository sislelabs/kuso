package crons

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"kuso/server/internal/kube"
)

// ---- cross-project qualified-name access (finding 2) ---------------------
//
// CRName / the project-cron fqn ("<project>-<name>") accept an already-
// qualified name. With overlapping project names — "foo" and "foo-bar" —
// a member of foo passing name="foo-bar-svc-nightly" resolves to
// foo-bar's cron. Every path that fetches by that name must verify the
// FETCHED CR's spec.project (cronOwnedByProject) before acting.

func cronFakeService(t *testing.T, crons ...*kube.KusoCron) *Service {
	t.Helper()
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		kube.GVRCrons: "KusoCronList",
	})
	for _, c := range crons {
		m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(c)
		if err != nil {
			t.Fatalf("encode cron: %v", err)
		}
		u := &unstructured.Unstructured{Object: m}
		u.SetGroupVersionKind(kube.GVRCrons.GroupVersion().WithKind("KusoCron"))
		if u.GetNamespace() == "" {
			u.SetNamespace("kuso")
		}
		if err := dyn.Tracker().Create(kube.GVRCrons, u, "kuso"); err != nil {
			t.Fatalf("seed cron: %v", err)
		}
	}
	return &Service{Kube: &kube.Client{Dynamic: dyn}, Namespace: "kuso"}
}

// victimServiceCron builds foo-bar's service cron "foo-bar-svc-nightly".
func victimServiceCron() *kube.KusoCron {
	return &kube.KusoCron{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-bar-svc-nightly",
			Namespace: "kuso",
			Labels:    map[string]string{"kuso.sislelabs.com/project": "foo-bar"},
		},
		Spec: kube.KusoCronSpec{
			Project:  "foo-bar",
			Kind:     "service",
			Service:  "foo-bar-svc",
			Schedule: "0 0 * * *",
			Command:  []string{"echo", "hi"},
		},
	}
}

// victimProjectCron builds foo-bar's project cron "foo-bar-report".
func victimProjectCron() *kube.KusoCron {
	return &kube.KusoCron{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-bar-report",
			Namespace: "kuso",
			Labels:    map[string]string{"kuso.sislelabs.com/project": "foo-bar"},
		},
		Spec: kube.KusoCronSpec{
			Project:  "foo-bar",
			Kind:     "http",
			URL:      "https://example.com/report",
			Schedule: "0 6 * * *",
		},
	}
}

func TestCron_Get_RejectsCrossProjectQualifiedName(t *testing.T) {
	t.Parallel()
	s := cronFakeService(t, victimServiceCron())
	ctx := context.Background()

	// Attacker on "foo" passes service "bar-svc" + name "nightly" →
	// CRName resolves "foo-bar-svc-nightly", foo-bar's cron.
	if _, err := s.Get(ctx, "foo", "bar-svc", "nightly"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get cross-tenant: want ErrNotFound, got %v", err)
	}
	// The already-qualified form is just as blocked.
	if _, err := s.Get(ctx, "foo", "svc", "foo-bar-svc-nightly"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get qualified cross-tenant: want ErrNotFound, got %v", err)
	}
	// Legitimate owner still resolves it.
	if _, err := s.Get(ctx, "foo-bar", "svc", "nightly"); err != nil {
		t.Errorf("Get owner: %v", err)
	}
}

func TestCron_Update_RejectsCrossProjectQualifiedName(t *testing.T) {
	t.Parallel()
	s := cronFakeService(t, victimServiceCron())
	ctx := context.Background()
	sched := "*/5 * * * *"

	_, err := s.Update(ctx, "foo", "bar-svc", "nightly", UpdateCronRequest{Schedule: &sched})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Update cross-tenant: want ErrNotFound, got %v", err)
	}
	// Victim untouched.
	cr, gerr := s.Get(ctx, "foo-bar", "svc", "nightly")
	if gerr != nil {
		t.Fatalf("victim cron gone: %v", gerr)
	}
	if cr.Spec.Schedule == sched {
		t.Errorf("victim cron mutated: %+v", cr.Spec)
	}

	// Owner can update.
	if _, err := s.Update(ctx, "foo-bar", "svc", "nightly", UpdateCronRequest{Schedule: &sched}); err != nil {
		t.Errorf("Update owner: %v", err)
	}
}

func TestCron_Delete_RejectsCrossProjectQualifiedName(t *testing.T) {
	t.Parallel()
	s := cronFakeService(t, victimServiceCron())
	ctx := context.Background()

	if err := s.Delete(ctx, "foo", "bar-svc", "nightly"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete cross-tenant: want ErrNotFound, got %v", err)
	}
	// Victim still present.
	if _, err := s.Get(ctx, "foo-bar", "svc", "nightly"); err != nil {
		t.Fatalf("victim cron deleted: %v", err)
	}
	// Owner can delete.
	if err := s.Delete(ctx, "foo-bar", "svc", "nightly"); err != nil {
		t.Errorf("Delete owner: %v", err)
	}
}

func TestCron_SyncFromService_RejectsCrossProjectQualifiedName(t *testing.T) {
	t.Parallel()
	s := cronFakeService(t, victimServiceCron())
	// Cross-tenant should 404 at the ownership guard before it even tries
	// to resolve the production env.
	if _, err := s.SyncFromService(context.Background(), "foo", "bar-svc", "nightly"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SyncFromService cross-tenant: want ErrNotFound, got %v", err)
	}
}

func TestCron_UpdateProject_RejectsCrossProjectQualifiedName(t *testing.T) {
	t.Parallel()
	s := cronFakeService(t, victimProjectCron())
	ctx := context.Background()
	sched := "*/10 * * * *"

	// Attacker on "foo" passes name "bar-report" → fqn "foo-bar-report".
	_, err := s.UpdateProject(ctx, "foo", "bar-report", UpdateProjectCronRequest{Schedule: &sched})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateProject cross-tenant: want ErrNotFound, got %v", err)
	}
	cr, gerr := s.Kube.GetKusoCron(ctx, "kuso", "foo-bar-report")
	if gerr != nil {
		t.Fatalf("victim project cron gone: %v", gerr)
	}
	if cr.Spec.Schedule == sched {
		t.Errorf("victim project cron mutated: %+v", cr.Spec)
	}

	// Owner can update.
	if _, err := s.UpdateProject(ctx, "foo-bar", "report", UpdateProjectCronRequest{Schedule: &sched}); err != nil {
		t.Errorf("UpdateProject owner: %v", err)
	}
}

func TestCron_DeleteProject_RejectsCrossProjectQualifiedName(t *testing.T) {
	t.Parallel()
	s := cronFakeService(t, victimProjectCron())
	ctx := context.Background()

	if err := s.DeleteProject(ctx, "foo", "bar-report"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteProject cross-tenant: want ErrNotFound, got %v", err)
	}
	if _, err := s.Kube.GetKusoCron(ctx, "kuso", "foo-bar-report"); err != nil {
		t.Fatalf("victim project cron deleted: %v", err)
	}
	if err := s.DeleteProject(ctx, "foo-bar", "report"); err != nil {
		t.Errorf("DeleteProject owner: %v", err)
	}
}
