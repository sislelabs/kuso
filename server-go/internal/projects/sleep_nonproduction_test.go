package projects

import (
	"context"
	"errors"
	"testing"

	"kuso/server/internal/kube"
)

func sleepFixture(t *testing.T) *Service {
	t.Helper()
	return fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Sleep: &kube.KusoServiceSleep{Enabled: true, AfterMinutes: 15}}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)
}

func TestPatchService_SleepNonProduction(t *testing.T) {
	t.Parallel()
	s := sleepFixture(t)
	off := "off"
	got, err := s.PatchService(context.Background(), "alpha", "web", PatchServiceRequest{Sleep: &PatchSleepRequest{NonProduction: &off}})
	if err != nil {
		t.Fatalf("PatchService: %v", err)
	}
	if got.Spec.Sleep == nil || got.Spec.Sleep.NonProduction != "off" {
		t.Fatalf("sleep = %+v, want nonProduction=off", got.Spec.Sleep)
	}
	// Untouched sleep fields survive.
	if !got.Spec.Sleep.Enabled || got.Spec.Sleep.AfterMinutes != 15 {
		t.Fatalf("sleep = %+v, want enabled/15 kept", got.Spec.Sleep)
	}

	on := "on"
	got, err = s.PatchService(context.Background(), "alpha", "web", PatchServiceRequest{Sleep: &PatchSleepRequest{NonProduction: &on}})
	if err != nil || got.Spec.Sleep.NonProduction != "on" {
		t.Fatalf("patch on = %+v, %v", got.Spec.Sleep, err)
	}

	// Omitted leaves it alone.
	enabled := false
	got, err = s.PatchService(context.Background(), "alpha", "web", PatchServiceRequest{Sleep: &PatchSleepRequest{Enabled: &enabled}})
	if err != nil || got.Spec.Sleep.NonProduction != "on" {
		t.Fatalf("patch without nonProduction = %+v, %v; want on kept", got.Spec.Sleep, err)
	}
}

func TestPatchService_SleepNonProductionRejectsJunk(t *testing.T) {
	t.Parallel()
	s := sleepFixture(t)
	bad := "sometimes"
	_, err := s.PatchService(context.Background(), "alpha", "web", PatchServiceRequest{Sleep: &PatchSleepRequest{NonProduction: &bad}})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	svc, _ := s.GetService(context.Background(), "alpha", "web")
	if svc.Spec.Sleep.NonProduction != "" {
		t.Fatalf("rejected value was written: %+v", svc.Spec.Sleep)
	}
}

func TestAddService_SleepNonProduction(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}))
	created, err := s.AddService(context.Background(), "alpha", CreateServiceRequest{
		Name: "web", Runtime: "dockerfile", Sleep: &ServiceSleep{NonProduction: "off"},
	})
	if err != nil {
		t.Fatalf("AddService: %v", err)
	}
	if created.Spec.Sleep == nil || created.Spec.Sleep.NonProduction != "off" {
		t.Fatalf("sleep = %+v, want nonProduction=off", created.Spec.Sleep)
	}
	_, err = s.AddService(context.Background(), "alpha", CreateServiceRequest{
		Name: "api", Runtime: "dockerfile", Sleep: &ServiceSleep{NonProduction: "maybe"},
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("junk nonProduction on create: err = %v, want ErrInvalid", err)
	}
}
