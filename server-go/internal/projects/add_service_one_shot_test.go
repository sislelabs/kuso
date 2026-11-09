package projects

import (
	"context"
	"errors"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// The create wizard used to POST the service and then PUT each variable
// and the addon subscription, so the first deploy rolled twice. One
// create must now leave the production env with all of it, and nothing
// may touch the env afterwards.
func TestAddService_EnvValuesAndSubscriptionInOneCreate(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("alpha", kube.KusoProjectSpec{BaseDomain: "alpha.example.com"}))
	cs := k8sfake.NewSimpleClientset()
	s.Kube.Clientset = cs
	s.AddonConnSecrets = func(context.Context, string) ([]string, error) {
		return []string{"alpha-cache-conn", "alpha-db-conn"}, nil
	}
	ctx := context.Background()
	subs := []string{" db ", "db"}

	_, err := s.AddService(ctx, "alpha", CreateServiceRequest{
		Name:      "web",
		Runtime:   "image",
		Image:     &ServiceImageSpec{Repository: "traefik/whoami", Tag: "v1"},
		PublicEnv: []string{"NEXT_PUBLIC_SITE"},
		EnvValues: []EnvValue{
			{Name: "GREETING", Value: "hello"},
			{Name: "DB", Value: "${{ db.DATABASE_URL }}"},
			{Name: "NEXT_PUBLIC_SITE", Value: "https://x.example"},
		},
		SubscribedAddons: &subs,
	})
	if err != nil {
		t.Fatalf("AddService: %v", err)
	}

	svc, err := s.GetService(ctx, "alpha", "web")
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	if !slices.Equal(svc.Spec.SubscribedAddons, []string{"db"}) {
		t.Errorf("service subscribedAddons = %v, want [db]", svc.Spec.SubscribedAddons)
	}
	env, err := s.GetEnvironment(ctx, "alpha", "alpha-web-production")
	if err != nil {
		t.Fatalf("production env: %v", err)
	}
	if !slices.Equal(env.Spec.SubscribedAddons, []string{"db"}) {
		t.Errorf("env subscribedAddons = %v, want [db]", env.Spec.SubscribedAddons)
	}
	if !slices.Contains(env.Spec.EnvFromSecrets, "alpha-db-conn") || slices.Contains(env.Spec.EnvFromSecrets, "alpha-cache-conn") {
		t.Errorf("env envFromSecrets = %v, want alpha-db-conn mounted and alpha-cache-conn not", env.Spec.EnvFromSecrets)
	}
	if !slices.Contains(env.Spec.EnvFromSecrets, kube.ServiceSecretName("alpha", "web")) {
		t.Errorf("env envFromSecrets = %v, want the managed service secret mounted", env.Spec.EnvFromSecrets)
	}

	byName := map[string]kube.KusoEnvVar{}
	for _, v := range env.Spec.EnvVars {
		byName[v.Name] = v
	}
	if _, onCR := byName["GREETING"]; onCR {
		t.Errorf("GREETING is on the env CR; a plain value belongs in the managed secret")
	}
	if v := byName["NEXT_PUBLIC_SITE"]; v.Value != "https://x.example" {
		t.Errorf("NEXT_PUBLIC_SITE = %+v, want a literal (the build reads it)", v)
	}
	if v := byName["DB"]; v.ValueFrom == nil {
		t.Errorf("DB = %+v, want a secretKeyRef from the ${{ db.DATABASE_URL }} reference", v)
	}
	sec, err := cs.CoreV1().Secrets("kuso").Get(ctx, kube.ServiceSecretName("alpha", "web"), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("managed secret: %v", err)
	}
	if string(sec.Data["GREETING"]) != "hello" {
		t.Errorf("managed secret GREETING = %q, want hello", sec.Data["GREETING"])
	}

	dyn, ok := s.Kube.Dynamic.(*dynamicfake.FakeDynamicClient)
	if !ok {
		t.Fatalf("dynamic client is %T", s.Kube.Dynamic)
	}
	for _, a := range dyn.Actions() {
		if a.GetResource() == kube.GVREnvironments && (a.GetVerb() == "update" || a.GetVerb() == "patch") {
			t.Errorf("production env was %sd after create; it must be born complete", a.GetVerb())
		}
	}
}

func TestAddService_EnvValuesRejectBadNamesBeforeCreating(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for name, vals := range map[string][]EnvValue{
		"invalid":   {{Name: "1BAD", Value: "x"}},
		"reserved":  {{Name: "KUBERNETES_SERVICE_HOST", Value: "x"}},
		"duplicate": {{Name: "A", Value: "1"}, {Name: "A", Value: "2"}},
	} {
		s := fakeService(t, seedProject("alpha", kube.KusoProjectSpec{}))
		s.Kube.Clientset = k8sfake.NewSimpleClientset()
		_, err := s.AddService(ctx, "alpha", CreateServiceRequest{Name: "web", Runtime: "dockerfile", EnvValues: vals})
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
		if _, gerr := s.GetService(ctx, "alpha", "web"); gerr == nil {
			t.Errorf("%s: service was created despite the bad variable", name)
		}
	}
}

// An empty list means "mount no addon" and must survive as [] (nil would
// mean mount-all).
func TestAddService_EmptySubscriptionMountsNoAddon(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("alpha", kube.KusoProjectSpec{}))
	s.AddonConnSecrets = func(context.Context, string) ([]string, error) { return []string{"alpha-db-conn"}, nil }
	none := []string{}
	if _, err := s.AddService(context.Background(), "alpha", CreateServiceRequest{Name: "web", Runtime: "dockerfile", SubscribedAddons: &none}); err != nil {
		t.Fatalf("AddService: %v", err)
	}
	env, err := s.GetEnvironment(context.Background(), "alpha", "alpha-web-production")
	if err != nil {
		t.Fatalf("production env: %v", err)
	}
	if env.Spec.SubscribedAddons == nil || len(env.Spec.SubscribedAddons) != 0 {
		t.Errorf("env subscribedAddons = %#v, want empty non-nil", env.Spec.SubscribedAddons)
	}
	if slices.Contains(env.Spec.EnvFromSecrets, "alpha-db-conn") {
		t.Errorf("env envFromSecrets = %v, want no addon conn", env.Spec.EnvFromSecrets)
	}
}
