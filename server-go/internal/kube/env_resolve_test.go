package kube

import (
	"context"
	"errors"
	"testing"
)

func envSeed(name string, labels map[string]string, project, service string) seedObj {
	s := seed(GVREnvironments, "KusoEnvironment", "kuso-puncho", name, map[string]any{
		"project": project,
		"service": service,
	})
	s.obj.SetLabels(labels)
	return s
}

// An env-group ("staging") clones web into KusoService puncho-web-staging
// whose env is puncho-web-staging-production labelled env=staging. Logs,
// env list and pods for `web --env staging` 404'd because they looked for
// a CR named puncho-web-staging.
func TestResolveServiceEnv(t *testing.T) {
	c := fakeClient(t,
		envSeed("puncho-web-production", map[string]string{LabelProject: "puncho", LabelService: "web", LabelEnv: "production"}, "puncho", "puncho-web"),
		envSeed("puncho-web-staging-production", map[string]string{LabelProject: "puncho", LabelService: "web-staging", LabelEnv: "staging"}, "puncho", "puncho-web-staging"),
		envSeed("puncho-web-pr-7", map[string]string{LabelProject: "puncho", LabelService: "web", LabelEnv: "preview-pr-7"}, "puncho", "puncho-web"),
		envSeed("puncho-api-production", nil, "puncho", "puncho-api"),
		// site has its own staging env AND a clone: its own env wins.
		envSeed("puncho-site-staging", map[string]string{LabelProject: "puncho", LabelService: "site", LabelEnv: "staging"}, "puncho", "puncho-site"),
		envSeed("puncho-site-staging-production", map[string]string{LabelProject: "puncho", LabelService: "site-staging", LabelEnv: "staging"}, "puncho", "puncho-site-staging"),
		// Same-namespace CR named like a puncho env but owned elsewhere.
		envSeed("puncho-web-qa", map[string]string{LabelProject: "puncho", LabelEnv: "qa"}, "other", "puncho-web"),
		envSeed("puncho-web-qa-production", map[string]string{LabelProject: "puncho", LabelEnv: "qa"}, "other", "puncho-web-qa"),
	)
	ctx := context.Background()
	cases := []struct{ service, env, want string }{
		{"web", "staging", "puncho-web-staging-production"},
		{"web", "", "puncho-web-production"},
		{"web", "production", "puncho-web-production"},
		{"web-staging", "production", "puncho-web-staging-production"},
		{"web-staging", "staging", "puncho-web-staging-production"},
		{"puncho-web-staging", "production", "puncho-web-staging-production"},
		{"site", "staging", "puncho-site-staging"},
		{"web", "preview-pr-7", "puncho-web-pr-7"},
		{"web", "puncho-web-production", "puncho-web-production"},
		{"api", "production", "puncho-api-production"},
		{"web", "qa", ""},
		{"api", "staging", ""},
		{"api", "puncho-web-production", ""},
	}
	for _, tc := range cases {
		got, err := c.ResolveServiceEnv(ctx, "kuso-puncho", "puncho", tc.service, tc.env)
		if tc.want == "" {
			if !errors.Is(err, ErrEnvNotFound) {
				t.Errorf("%s --env %q: want ErrEnvNotFound, got %v (%v)", tc.service, tc.env, got, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s --env %q: %v", tc.service, tc.env, err)
			continue
		}
		if got.Name != tc.want {
			t.Errorf("%s --env %q: got %s, want %s", tc.service, tc.env, got.Name, tc.want)
		}
	}
}

func TestServiceEnvRef(t *testing.T) {
	c := fakeClient(t, envSeed("puncho-web-staging-production", nil, "puncho", "puncho-web-staging"))
	e, err := c.GetKusoEnvironment(context.Background(), "kuso-puncho", "puncho-web-staging-production")
	if err != nil {
		t.Fatal(err)
	}
	short, suffix, ok := ServiceEnvRef("puncho", e)
	if !ok || short != "web-staging" || suffix != "production" {
		t.Fatalf("got (%q, %q, %v)", short, suffix, ok)
	}
}
