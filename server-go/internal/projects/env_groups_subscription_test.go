package projects

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"kuso/server/internal/kube"
)

// An env-group clone must mount what its SOURCE service mounts: only the
// clones of subscribed addons, and no blanket shared-secret envFrom when the
// service opted into no shared keys. Live e2e: web (subscribedAddons=[],
// sharedEnvKeys=[]) came up in group qa holding DATABASE_URL, REDIS_URL and
// every shared key.
func TestCreateEnvGroup_HonoursSourceSubscriptions(t *testing.T) {
	t.Parallel()

	s := fakeService(t,
		seedProject("acme", kube.KusoProjectSpec{BaseDomain: "apps.example.com"}),
		seedService("acme", "web", kube.KusoServiceSpec{Project: "acme", Port: 8080, SubscribedAddons: []string{}, SharedEnvKeys: []string{}}),
		seedEnv("acme", "web", "production", "main", "acme-web-production"),
		seedService("acme", "api", kube.KusoServiceSpec{Project: "acme", Port: 8080, SubscribedAddons: []string{"db"}}),
		seedEnv("acme", "api", "production", "main", "acme-api-production"),
		seedService("acme", "legacy", kube.KusoServiceSpec{Project: "acme", Port: 8080}),
		seedEnv("acme", "legacy", "production", "main", "acme-legacy-production"),
		seedAddon("acme", "db", "postgres"),
		seedAddon("acme", "cache", "redis"),
	)
	s.AddonConnSecrets = func(ctx context.Context, project string) ([]string, error) {
		return []string{"acme-cache-conn", "acme-db-conn"}, nil
	}

	if _, err := s.CreateEnvGroup(context.Background(), "acme", CreateEnvGroupRequest{Name: "qa"}); err != nil {
		t.Fatalf("CreateEnvGroup: %v", err)
	}
	envFrom := func(svcShort string) []string {
		t.Helper()
		env, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "acme-"+svcShort+"-qa-production")
		if err != nil {
			t.Fatalf("get %s env: %v", svcShort, err)
		}
		return env.Spec.EnvFromSecrets
	}
	shared := kube.SharedSecretNames("acme")
	check := func(svcShort string, want, notWant []string) {
		t.Helper()
		efs := envFrom(svcShort)
		for _, w := range want {
			if !slices.Contains(efs, w) {
				t.Errorf("%s-qa: want %s mounted: %v", svcShort, w, efs)
			}
		}
		for _, nw := range append(notWant, "acme-db-conn", "acme-cache-conn") {
			if slices.Contains(efs, nw) {
				t.Errorf("%s-qa: %s must not be mounted: %v", svcShort, nw, efs)
			}
		}
	}
	check("web", nil, append([]string{"acme-db-qa-conn", "acme-cache-qa-conn"}, shared...))
	check("api", append([]string{"acme-db-qa-conn"}, shared...), []string{"acme-cache-qa-conn"})
	check("legacy", append([]string{"acme-db-qa-conn", "acme-cache-qa-conn"}, shared...), nil)

	// nil-vs-empty must survive the clone on both the service and env CR.
	svc, err := s.Kube.GetKusoService(context.Background(), "kuso", "acme-web-qa")
	if err != nil {
		t.Fatalf("get cloned service: %v", err)
	}
	if svc.Spec.SubscribedAddons == nil || svc.Spec.SharedEnvKeys == nil {
		t.Errorf("cloned service collapsed [] to nil: subscribed=%#v shared=%#v", svc.Spec.SubscribedAddons, svc.Spec.SharedEnvKeys)
	}
	env, _ := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "acme-web-qa-production")
	if env.Spec.SubscribedAddons == nil || env.Spec.SharedEnvKeys == nil {
		t.Errorf("cloned env collapsed [] to nil: subscribed=%#v shared=%#v", env.Spec.SubscribedAddons, env.Spec.SharedEnvKeys)
	}
}

// An env-group reuses production's image but gets FRESH databases, so the
// release hook (migrations) must run against them or the clone serves an
// empty schema (live: api-qa -> `relation "todo" does not exist`). Only envs
// that inherited an image, have a release command and actually use a fresh
// addon qualify.
func TestCreateEnvGroup_RunsReleaseHookAgainstFreshAddons(t *testing.T) {
	t.Parallel()

	migrate := &kube.KusoReleaseSpec{Command: []string{"api", "migrate"}}
	s := fakeService(t,
		seedProject("acme", kube.KusoProjectSpec{BaseDomain: "apps.example.com"}),
		seedService("acme", "api", kube.KusoServiceSpec{Project: "acme", Port: 8080, SubscribedAddons: []string{"db"}, Release: migrate}),
		seedEnv("acme", "api", "production", "main", "acme-api-production"),
		seedService("acme", "web", kube.KusoServiceSpec{Project: "acme", Port: 8080, SubscribedAddons: []string{}}),
		seedEnv("acme", "web", "production", "main", "acme-web-production"),
		seedService("acme", "bot", kube.KusoServiceSpec{Project: "acme", Port: 8080, SubscribedAddons: []string{}, Release: migrate}),
		seedEnv("acme", "bot", "production", "main", "acme-bot-production"),
		seedAddon("acme", "db", "postgres"),
	)
	s.AddonConnSecrets = func(ctx context.Context, project string) ([]string, error) {
		return []string{"acme-db-conn"}, nil
	}
	for _, name := range []string{"acme-api-production", "acme-web-production", "acme-bot-production"} {
		env, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", name)
		if err != nil {
			t.Fatalf("get %s: %v", name, err)
		}
		env.Spec.Image = &kube.KusoImage{Repository: "registry/acme", Tag: "abc123"}
		if _, err := s.Kube.UpdateKusoEnvironment(context.Background(), "kuso", env); err != nil {
			t.Fatalf("update %s: %v", name, err)
		}
	}
	var released []string
	s.RunEnvRelease = func(env *kube.KusoEnvironment) {
		if env.Spec.Image == nil || env.Spec.Image.Tag != "abc123" {
			t.Errorf("release for %s without the inherited image: %+v", env.Name, env.Spec.Image)
		}
		released = append(released, env.Name)
	}

	if _, err := s.CreateEnvGroup(context.Background(), "acme", CreateEnvGroupRequest{Name: "qa"}); err != nil {
		t.Fatalf("CreateEnvGroup: %v", err)
	}
	if !slices.Equal(released, []string{"acme-api-qa-production"}) {
		t.Errorf("release hook ran for %v, want only [acme-api-qa-production]", released)
	}
}

// ISO-3: a group clone inherits the source's project shared keys with
// production's value, so creation says which ones; sharedKeys=none clones
// with no shared subscription (and no wholesale shared mount for a legacy
// nil-subscription service).
func TestCreateEnvGroup_SharedKeyInheritance(t *testing.T) {
	t.Parallel()
	shared := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "acme-shared", Namespace: "kuso"},
		Data: map[string][]byte{"STRIPE_KEY": []byte("sk_live"), "OTHER": []byte("x")}}
	s := fakeServiceWithSecrets(t, []runtime.Object{shared},
		seedProject("acme", kube.KusoProjectSpec{BaseDomain: "apps.example.com"}),
		seedService("acme", "web", kube.KusoServiceSpec{Project: "acme", Port: 8080, SharedEnvKeys: []string{"STRIPE_KEY"}}),
		seedEnv("acme", "web", "production", "main", "acme-web-production"),
		seedService("acme", "legacy", kube.KusoServiceSpec{Project: "acme", Port: 8080}),
		seedEnv("acme", "legacy", "production", "main", "acme-legacy-production"),
	)
	ctx := context.Background()
	sum, err := s.CreateEnvGroup(ctx, "acme", CreateEnvGroupRequest{Name: "qa"})
	if err != nil {
		t.Fatal(err)
	}
	warned := func(prefix, keys string) bool {
		for _, w := range sum.Warnings {
			if strings.HasPrefix(w, prefix+": ") && strings.Contains(w, "shared secret(s) inherited") && strings.Contains(w, "("+keys+")") {
				return true
			}
		}
		return false
	}
	if !warned("web-qa", "STRIPE_KEY") || !warned("legacy-qa", "OTHER, STRIPE_KEY") {
		t.Errorf("inherited shared keys not warned: %q", sum.Warnings)
	}

	sum, err = s.CreateEnvGroup(ctx, "acme", CreateEnvGroupRequest{Name: "demo", SharedKeys: SharedKeysNone})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range sum.Warnings {
		if strings.Contains(w, "shared secret(s) inherited") {
			t.Errorf("sharedKeys=none still warns: %q", w)
		}
	}
	for _, short := range []string{"web", "legacy"} {
		svc, err := s.Kube.GetKusoService(ctx, "kuso", "acme-"+short+"-demo")
		if err != nil {
			t.Fatal(err)
		}
		if svc.Spec.SharedEnvKeys == nil || len(svc.Spec.SharedEnvKeys) != 0 {
			t.Errorf("%s-demo sharedEnvKeys = %#v, want []", short, svc.Spec.SharedEnvKeys)
		}
		env, err := s.Kube.GetKusoEnvironment(ctx, "kuso", "acme-"+short+"-demo-production")
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range env.Spec.EnvVars {
			if v.Name == "STRIPE_KEY" {
				t.Errorf("%s-demo still references STRIPE_KEY: %+v", short, v)
			}
		}
		for _, n := range kube.SharedSecretNames("acme") {
			if slices.Contains(env.Spec.EnvFromSecrets, n) {
				t.Errorf("%s-demo mounts %s: %v", short, n, env.Spec.EnvFromSecrets)
			}
		}
	}
	if _, err := s.CreateEnvGroup(ctx, "acme", CreateEnvGroupRequest{Name: "bad", SharedKeys: "some"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad sharedKeys: err = %v", err)
	}
}
