package projects

import (
	"context"
	"errors"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// seedPreviewEnv seeds a preview KusoEnvironment with the given TTL
// expiry and the env-group label the dispatcher stamps.
func seedPreviewEnv(project, service, name string, prGroup string, expiresAt time.Time) seed {
	return typedSeed(kube.GVREnvironments, "KusoEnvironment", name, &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "kuso",
			Labels: map[string]string{
				labelProject: project,
				labelService: service,
				labelEnv:     prGroup,
			},
		},
		Spec: kube.KusoEnvironmentSpec{
			Project: project,
			Service: project + "-" + service,
			Kind:    "preview",
			TTL:     &kube.KusoTTL{ExpiresAt: expiresAt.UTC().Format(time.RFC3339)},
		},
	})
}

func getEnv(t *testing.T, s *Service, name string) *kube.KusoEnvironment {
	t.Helper()
	e, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", name)
	if err != nil {
		return nil
	}
	return e
}

// TestSweepExpiredPreviews_OpenPRExtendsTTL: an expired preview whose
// PR is still open must be kept and its expiresAt pushed forward by
// the project's ttlDays, not deleted.
func TestSweepExpiredPreviews_OpenPRExtendsTTL(t *testing.T) {
	t.Parallel()
	past := time.Now().Add(-2 * time.Hour)
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{
			Previews: &kube.KusoPreviewsSpec{Enabled: true, TTLDays: 3},
		}),
		seedPreviewEnv("alpha", "web", "alpha-web-pr-9", "preview-pr-9", past),
	)
	var askedProject string
	var askedPR int
	s.PreviewPROpen = func(_ context.Context, project string, pr int) (bool, error) {
		askedProject, askedPR = project, pr
		return true, nil
	}

	deleted, err := s.SweepExpiredPreviews(context.Background(), nil)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if deleted != 0 {
		t.Errorf("deleted = %d, want 0 (PR is open)", deleted)
	}
	if askedProject != "alpha" || askedPR != 9 {
		t.Errorf("checker called with (%q, %d), want (alpha, 9)", askedProject, askedPR)
	}
	e := getEnv(t, s, "alpha-web-pr-9")
	if e == nil {
		t.Fatal("env was deleted despite open PR")
	}
	exp, perr := time.Parse(time.RFC3339, e.Spec.TTL.ExpiresAt)
	if perr != nil {
		t.Fatalf("parse extended expiresAt: %v", perr)
	}
	// Extended by the project's 3-day TTL (not the 7-day default).
	want := time.Now().Add(3 * 24 * time.Hour)
	if diff := exp.Sub(want); diff < -time.Hour || diff > time.Hour {
		t.Errorf("expiresAt = %s, want ~%s (ttlDays=3)", exp, want)
	}
}

// TestSweepExpiredPreviews_ClosedPRDeletes: PR confirmed closed →
// the expired env is torn down as before.
func TestSweepExpiredPreviews_ClosedPRDeletes(t *testing.T) {
	t.Parallel()
	past := time.Now().Add(-2 * time.Hour)
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedPreviewEnv("alpha", "web", "alpha-web-pr-9", "preview-pr-9", past),
	)
	s.PreviewPROpen = func(_ context.Context, _ string, _ int) (bool, error) {
		return false, nil
	}

	deleted, err := s.SweepExpiredPreviews(context.Background(), nil)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1", deleted)
	}
	if getEnv(t, s, "alpha-web-pr-9") != nil {
		t.Error("env still present after closed-PR sweep")
	}
}

// TestSweepExpiredPreviews_CheckErrorKeepsEnv: a GitHub failure must
// NOT delete the env — keep it and let the next tick retry.
func TestSweepExpiredPreviews_CheckErrorKeepsEnv(t *testing.T) {
	t.Parallel()
	past := time.Now().Add(-2 * time.Hour)
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedPreviewEnv("alpha", "web", "alpha-web-pr-9", "preview-pr-9", past),
	)
	s.PreviewPROpen = func(_ context.Context, _ string, _ int) (bool, error) {
		return false, errors.New("github: 502")
	}

	var reported int
	deleted, err := s.SweepExpiredPreviews(context.Background(), func(string, error) { reported++ })
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if deleted != 0 {
		t.Errorf("deleted = %d, want 0 (check errored)", deleted)
	}
	if reported == 0 {
		t.Error("check error was not reported via onErr")
	}
	e := getEnv(t, s, "alpha-web-pr-9")
	if e == nil {
		t.Fatal("env deleted despite check error")
	}
	if got := e.Spec.TTL.ExpiresAt; got != past.UTC().Format(time.RFC3339) {
		t.Errorf("expiresAt changed on check error: %s", got)
	}
}

// TestSweepExpiredPreviews_NoCheckerLegacyDelete: nil checker (no
// GitHub App) → pre-existing delete-on-expiry behaviour.
func TestSweepExpiredPreviews_NoCheckerLegacyDelete(t *testing.T) {
	t.Parallel()
	past := time.Now().Add(-2 * time.Hour)
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedPreviewEnv("alpha", "web", "alpha-web-pr-9", "preview-pr-9", past),
	)

	deleted, err := s.SweepExpiredPreviews(context.Background(), nil)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1 (legacy path)", deleted)
	}
}

func TestPreviewPRNumberFromEnv(t *testing.T) {
	t.Parallel()
	cases := []struct {
		label, name string
		want        int
		ok          bool
	}{
		{"preview-pr-46", "tickero-api-pr-46", 46, true},
		{"", "tickero-api-pr-12", 12, true}, // pre-label CR: name fallback
		{"staging", "tickero-api-staging", 0, false},
		{"preview-pr-x", "alpha-web", 0, false},
	}
	for _, c := range cases {
		e := &kube.KusoEnvironment{
			ObjectMeta: metav1.ObjectMeta{
				Name:   c.name,
				Labels: map[string]string{labelEnv: c.label},
			},
		}
		got, ok := previewPRNumberFromEnv(e)
		if got != c.want || ok != c.ok {
			t.Errorf("previewPRNumberFromEnv(label=%q, name=%q) = (%d, %v), want (%d, %v)",
				c.label, c.name, got, ok, c.want, c.ok)
		}
	}
}
