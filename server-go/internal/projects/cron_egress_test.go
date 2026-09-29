package projects

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

func seedCron(name string, spec kube.KusoCronSpec) seed {
	return typedSeed(kube.GVRCrons, "KusoCron", name, &kube.KusoCron{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso"},
		Spec:       spec,
	})
}

func cronEgress(t *testing.T, s *Service, name string) (bool, bool) {
	t.Helper()
	c, err := s.Kube.GetKusoCron(context.Background(), "kuso", name)
	if err != nil {
		t.Fatalf("get cron %s: %v", name, err)
	}
	return c.Spec.PrivateEgress, c.Spec.PlatformAPIEgress
}

// Cron pods get their NetworkPolicy egress labels from the cron CR, not
// the service. Toggling privateEgress on a service must reach its existing
// crons, or a service made private keeps crons with internet egress.
func TestPatchService_EgressPropagatesToServiceCrons(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Port: 8080}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
		seedCron("alpha-web-nightly", kube.KusoCronSpec{Project: "alpha", Service: "alpha-web", Schedule: "0 0 * * *"}),
		seedCron("alpha-api-nightly", kube.KusoCronSpec{Project: "alpha", Service: "alpha-api", Schedule: "0 0 * * *"}),
		seedCron("alpha-ping", kube.KusoCronSpec{Project: "alpha", Kind: "http", URL: "https://x", Schedule: "0 0 * * *"}),
	)
	on := true
	if _, err := s.PatchService(context.Background(), "alpha", "web", PatchServiceRequest{
		PrivateEgress: &on, PlatformAPIEgress: &on,
	}); err != nil {
		t.Fatalf("PatchService: %v", err)
	}
	if p, a := cronEgress(t, s, "alpha-web-nightly"); !p || !a {
		t.Errorf("web cron: private=%v platform=%v, want true/true", p, a)
	}
	for _, other := range []string{"alpha-api-nightly", "alpha-ping"} {
		if p, a := cronEgress(t, s, other); p || a {
			t.Errorf("%s must be untouched, got private=%v platform=%v", other, p, a)
		}
	}
}

// Crons created before the cron CRD carried the egress fields read as
// privateEgress=false, which the chart now renders as public egress. The
// boot heal restamps them from their service so a private service's
// crons don't gain internet access on upgrade.
func TestHealCronEgress_RestampsFromService(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Port: 8080, PrivateEgress: true}),
		seedCron("alpha-web-nightly", kube.KusoCronSpec{Project: "alpha", Service: "alpha-web", Schedule: "0 0 * * *"}),
		seedCron("alpha-ping", kube.KusoCronSpec{Project: "alpha", Kind: "http", URL: "https://x", Schedule: "0 0 * * *"}),
	)
	s.HealCronEgress(context.Background(), nil)
	if p, _ := cronEgress(t, s, "alpha-web-nightly"); !p {
		t.Error("heal did not restamp privateEgress=true onto the private service's cron")
	}
	if p, _ := cronEgress(t, s, "alpha-ping"); p {
		t.Error("heal must not touch project-scoped crons")
	}
}
