package addons

import (
	"context"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

func isolationAddon(name, kind string, labels map[string]string, external bool) seed {
	l := map[string]string{"kuso.sislelabs.com/project": "tickero"}
	for k, v := range labels {
		l[k] = v
	}
	spec := kube.KusoAddonSpec{Project: "tickero", Kind: kind}
	if external {
		spec.External = &kube.KusoAddonExternal{SecretName: name + "-src"}
	}
	return typedSeed(kube.GVRAddons, "KusoAddon", &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{Name: "tickero-" + name, Namespace: "kuso", Labels: l},
		Spec:       spec,
	})
}

func seedPreviewEnv(service string, pr string, envFrom []string) seed {
	return typedSeed(kube.GVREnvironments, "KusoEnvironment", &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tickero-" + service + "-pr-" + pr,
			Namespace: "kuso",
			Labels: map[string]string{
				"kuso.sislelabs.com/project": "tickero",
				"kuso.sislelabs.com/service": service,
				"kuso.sislelabs.com/env":     "preview-pr-" + pr,
			},
		},
		Spec: kube.KusoEnvironmentSpec{Project: "tickero", Service: "tickero-" + service, Kind: "preview", EnvFromSecrets: envFrom},
	})
}

func envFromOf(t *testing.T, s *Service, name string) []string {
	t.Helper()
	env, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", name)
	if err != nil {
		t.Fatalf("get %s: %v", name, err)
	}
	return env.Spec.EnvFromSecrets
}

// DATA-01: the refresh must never mount the production service secret
// (`kuso env set` values: live payment keys, admin tokens) on a preview.
func TestRefreshEnvSecrets_PreviewNeverGetsServiceSecret(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProj("tickero"),
		seedEnv("tickero", "api", "production", "tickero-api-production"),
		seedPreviewEnv("api", "52", []string{"tickero-shared"}),
	)
	if err := s.RefreshEnvSecrets(context.Background(), "tickero"); err != nil {
		t.Fatalf("RefreshEnvSecrets: %v", err)
	}
	if got := envFromOf(t, s, "tickero-api-pr-52"); slices.Contains(got, "tickero-api-secrets") {
		t.Fatalf("preview got the production service secret: %v", got)
	}
	if got := envFromOf(t, s, "tickero-api-production"); !slices.Contains(got, "tickero-api-secrets") {
		t.Fatalf("production lost its service secret: %v", got)
	}
}

// DATA-03/DATA-04: a preview never mounts a production conn it has no clone
// of, except the kinds shared by design (s3, mailpit).
func TestRefreshEnvSecrets_PreviewWithholdsUnclonedProductionConns(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProj("tickero"),
		isolationAddon("db", "postgres", nil, false),
		isolationAddon("db-pr-52", "postgres", map[string]string{kube.LabelEnv: "preview-pr-52"}, false),
		isolationAddon("psdb", "postgres", nil, true),       // external — never cloned
		isolationAddon("analytics", "postgres", nil, false), // added after the PR opened
		isolationAddon("events", "nats", nil, false),
		isolationAddon("storage", "s3", nil, false),
		seedPreviewEnv("api", "52", []string{"tickero-db-pr-52-conn"}),
	)
	if err := s.RefreshEnvSecrets(context.Background(), "tickero"); err != nil {
		t.Fatalf("RefreshEnvSecrets: %v", err)
	}
	got := envFromOf(t, s, "tickero-api-pr-52")
	for _, want := range []string{"tickero-db-pr-52-conn", "tickero-storage-conn"} {
		if !slices.Contains(got, want) {
			t.Errorf("preview missing %s: %v", want, got)
		}
	}
	for _, leak := range []string{"tickero-db-conn", "tickero-psdb-conn", "tickero-analytics-conn", "tickero-events-conn"} {
		if slices.Contains(got, leak) {
			t.Errorf("preview mounts production %s: %v", leak, got)
		}
	}
}

// DATA-04: a postgres/redis addon created after an isolated named env (one
// that owns clones) must not be added to it with the production conn. Conns
// the env already mounts (env-group "shared" policy) stay.
func TestRefreshEnvSecrets_IsolatedNamedEnvDoesNotGainNewCloneableConn(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProj("tickero"),
		isolationAddon("db", "postgres", nil, false),
		isolationAddon("db-staging", "postgres", map[string]string{kube.LabelEnv: "staging"}, false),
		isolationAddon("cache", "redis", nil, false), // shared with staging at group creation
		isolationAddon("analytics", "postgres", nil, false),
		isolationAddon("plain", "postgres", nil, false),
		seedEnvWithSecrets("tickero", "api", "staging", "tickero-api-staging", nil,
			[]string{"tickero-cache-conn", "tickero-db-staging-conn"}),
		// A named env without clones shares production by design.
		seedEnvWithSecrets("tickero", "api", "qa", "tickero-api-qa", nil, nil),
	)
	if err := s.RefreshEnvSecrets(context.Background(), "tickero"); err != nil {
		t.Fatalf("RefreshEnvSecrets: %v", err)
	}
	got := envFromOf(t, s, "tickero-api-staging")
	if slices.Contains(got, "tickero-analytics-conn") || slices.Contains(got, "tickero-plain-conn") || slices.Contains(got, "tickero-db-conn") {
		t.Errorf("isolated staging gained a production conn: %v", got)
	}
	if !slices.Contains(got, "tickero-cache-conn") || !slices.Contains(got, "tickero-db-staging-conn") {
		t.Errorf("staging lost an existing mount: %v", got)
	}
	if qa := envFromOf(t, s, "tickero-api-qa"); !slices.Contains(qa, "tickero-analytics-conn") {
		t.Errorf("non-isolated named env should keep sharing production: %v", qa)
	}
}
