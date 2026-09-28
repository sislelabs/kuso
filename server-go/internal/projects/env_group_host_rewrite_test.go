package projects

import (
	"context"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"kuso/server/internal/kube"
)

// Live e2e (#7): `env-group create e2e qa` cloned web → web-qa with
// API_BASE=https://api.e2e.sislelabs.com (production api) copied verbatim,
// so the qa frontend talked to production data. Literals naming a sibling's
// production host — in the service spec OR the managed <svc>-secrets copy —
// must land on the sibling's clone host, exact-host only, with the rewritten
// keys and the unmappable project-domain URLs reported back.
func TestCreateEnvGroup_RewritesSiblingProductionHosts(t *testing.T) {
	t.Parallel()
	const base = "e2e.example.com"
	webVars := []kube.KusoEnvVar{
		{Name: "API_BASE", Value: "https://api.e2e.example.com/v1"},
		{Name: "API_PUBLIC", Value: "https://api.acme.io:8443/x?y=1"},
		{Name: "APP_ENV", Value: "production"},
		{Name: "CDN", Value: "https://cdn.e2e.example.com/assets"},
		{Name: "LOOKALIKE", Value: "https://xapi.e2e.example.com.evil.io"},
		{Name: "SUBDOMAIN", Value: "https://v2.api.e2e.example.com"},
	}
	managed := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-web-secrets", Namespace: "kuso"},
		Data: map[string][]byte{
			"SECRET_API_URL": []byte("https://api.e2e.example.com"),
			"TOKEN":          []byte("s3cr3t"),
		},
	}
	s := fakeServiceWithSecrets(t, []runtime.Object{managed},
		seedProject("e2e", kube.KusoProjectSpec{BaseDomain: base}),
		seedService("e2e", "api", kube.KusoServiceSpec{Project: "e2e", Port: 8080,
			Domains: []kube.KusoDomain{{Host: "api.acme.io", TLS: true}}}),
		seedEnvDivergent("e2e", "api", "production", "production", "e2e-api-production", "api.e2e.example.com"),
		seedService("e2e", "web", kube.KusoServiceSpec{Project: "e2e", Port: 8080, EnvVars: webVars}),
		seedEnvDivergent("e2e", "web", "production", "production", "e2e-web-production", "web.e2e.example.com"),
	)

	sum, err := s.CreateEnvGroup(context.Background(), "e2e", CreateEnvGroupRequest{Name: "qa"})
	if err != nil {
		t.Fatalf("CreateEnvGroup: %v", err)
	}

	want := map[string]string{
		"API_BASE":   "https://api-qa.e2e.example.com/v1",
		"API_PUBLIC": "https://api-qa.e2e.example.com:8443/x?y=1",
		"APP_ENV":    "production",
		"LOOKALIKE":  "https://xapi.e2e.example.com.evil.io",
		"SUBDOMAIN":  "https://v2.api.e2e.example.com",
	}
	svc, err := s.Kube.GetKusoService(context.Background(), "kuso", "e2e-web-qa")
	if err != nil {
		t.Fatalf("get clone svc: %v", err)
	}
	env, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "e2e-web-qa-production")
	if err != nil {
		t.Fatalf("get clone env: %v", err)
	}
	for _, list := range [][]kube.KusoEnvVar{svc.Spec.EnvVars, env.Spec.EnvVars} {
		for _, v := range list {
			if w, ok := want[v.Name]; ok && v.Value != w {
				t.Errorf("%s = %q, want %q", v.Name, v.Value, w)
			}
		}
	}
	sec := getSecret(t, s, "kuso", "e2e-web-qa-secrets")
	if got := string(sec.Data["SECRET_API_URL"]); got != "https://api-qa.e2e.example.com" {
		t.Errorf("managed SECRET_API_URL = %q, want clone host", got)
	}
	if got := string(sec.Data["TOKEN"]); got != "s3cr3t" {
		t.Errorf("managed TOKEN = %q, must copy verbatim", got)
	}

	wantRewritten := []string{"web-qa: API_BASE", "web-qa: API_PUBLIC", "web-qa: SECRET_API_URL"}
	if !slices.Equal(sum.RewrittenEnvVars, wantRewritten) {
		t.Errorf("RewrittenEnvVars = %v, want %v", sum.RewrittenEnvVars, wantRewritten)
	}
	// CDN and SUBDOMAIN sit under the project's base domain but name no
	// project service's production host → warn. LOOKALIKE is off-domain,
	// APP_ENV isn't a URL; neither appears anywhere.
	var warnedKeys []string
	for _, w := range sum.Warnings {
		if strings.Contains(w, "s3cr3t") {
			t.Errorf("warning leaks a secret value: %q", w)
		}
		for _, k := range []string{"CDN", "SUBDOMAIN", "LOOKALIKE", "APP_ENV", "API_BASE"} {
			if strings.Contains(w, " "+k+" ") || strings.Contains(w, ": "+k+" ") {
				warnedKeys = append(warnedKeys, k)
			}
		}
	}
	slices.Sort(warnedKeys)
	if !slices.Equal(warnedKeys, []string{"CDN", "SUBDOMAIN"}) {
		t.Errorf("warned keys = %v (warnings %q), want [CDN SUBDOMAIN]", warnedKeys, sum.Warnings)
	}
}

func TestRewriteProdHosts_ExactHostOnly(t *testing.T) {
	t.Parallel()
	hosts := map[string]string{"acme.io": "web-qa.acme.io", "api.acme.io": "api-qa.acme.io"}
	cases := map[string]string{
		"https://api.acme.io/v1":             "https://api-qa.acme.io/v1",
		"https://acme.io":                    "https://web-qa.acme.io",
		"https://API.acme.io:443":            "https://api-qa.acme.io:443",
		"api.acme.io,acme.io":                "api-qa.acme.io,web-qa.acme.io",
		"https://my-api.acme.io":             "https://my-api.acme.io",
		"https://api.acme.io.evil.com/x":     "https://api.acme.io.evil.com/x",
		"production":                         "production",
		"postgres://u:p@db.other.io:5432/db": "postgres://u:p@db.other.io:5432/db",
	}
	for in, want := range cases {
		if got, _ := rewriteProdHosts(in, hosts); got != want {
			t.Errorf("rewriteProdHosts(%q) = %q, want %q", in, got, want)
		}
	}
}
