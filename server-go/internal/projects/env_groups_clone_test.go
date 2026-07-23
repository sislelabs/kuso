package projects

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"kuso/server/internal/kube"
)

// TestCreateEnvGroup_DoesNotInheritCustomDomains guards against the
// traffic-hijack bug where a cloned env stamped the SOURCE service's
// production custom domains into its own AdditionalHosts/TLSHosts (TLS on).
// The clone's Ingress would then claim production's host and race it for
// the same Let's Encrypt cert. The single-env / preview path NILs these;
// the clone path must too. Custom domains on a cloned env are an explicit
// opt-in via `kuso domains add`, never silent inheritance.
func TestCreateEnvGroup_DoesNotInheritCustomDomains(t *testing.T) {
	t.Parallel()

	s := fakeService(t,
		seedProject("acme", kube.KusoProjectSpec{BaseDomain: "apps.example.com"}),
		seedService("acme", "web", kube.KusoServiceSpec{
			Port: 8080,
			Domains: []kube.KusoDomain{
				{Host: "www.acme.com", TLS: true},
				{Host: "acme.com", TLS: true},
			},
		}),
		// Production env for the source service so the clone can inherit
		// the deployed image (path exercised in CreateEnvGroup).
		seedEnv("acme", "web", "production", "main", "acme-web-production"),
	)

	summary, err := s.CreateEnvGroup(context.Background(), "acme", CreateEnvGroupRequest{
		Name: "staging",
	})
	if err != nil {
		t.Fatalf("CreateEnvGroup: %v", err)
	}
	if summary == nil {
		t.Fatal("nil summary")
	}

	// The cloned env CR is named "<project>-<short>-<group>-production".
	clonedEnvName := "acme-web-staging-production"
	env, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", clonedEnvName)
	if err != nil {
		t.Fatalf("get cloned env %s: %v", clonedEnvName, err)
	}

	if len(env.Spec.AdditionalHosts) != 0 {
		t.Errorf("cloned env inherited source custom domains: AdditionalHosts = %v (want none)", env.Spec.AdditionalHosts)
	}
	// TLSHosts must only cover the clone's own generated host, never the
	// source service's custom domains.
	for _, h := range env.Spec.TLSHosts {
		if h == "www.acme.com" || h == "acme.com" {
			t.Errorf("cloned env TLSHosts leaked source custom domain %q: %v", h, env.Spec.TLSHosts)
		}
	}
}

// TestCreateEnvGroup_CarriesServiceSecrets guards against the clone
// dropping the service's OWN managed secrets. Production mounts
// <project>-<service>-secrets (app config/keys) via envFromSecrets; a
// clone that omitted it booted without app config and crash-looped at
// 0/N ready (the bukvite staging incident). The clone must carry the
// service-level secret + the env-scoped secret for the new env.
func TestCreateEnvGroup_CarriesServiceSecrets(t *testing.T) {
	t.Parallel()

	// Source service has a managed app-config secret (acme-web-secrets).
	srcSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: kube.ServiceSecretName("acme", "web"), Namespace: "kuso"},
		Data:       map[string][]byte{"APP_KEY": []byte("s3cr3t")},
	}
	s := fakeServiceWithSecrets(t, []runtime.Object{srcSecret},
		seedProject("acme", kube.KusoProjectSpec{BaseDomain: "apps.example.com"}),
		seedService("acme", "web", kube.KusoServiceSpec{Project: "acme", Port: 8080}),
		seedEnv("acme", "web", "production", "main", "acme-web-production"),
	)

	if _, err := s.CreateEnvGroup(context.Background(), "acme", CreateEnvGroupRequest{Name: "staging"}); err != nil {
		t.Fatalf("CreateEnvGroup: %v", err)
	}

	env, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "acme-web-staging-production")
	if err != nil {
		t.Fatalf("get cloned env: %v", err)
	}

	has := func(name string) bool {
		for _, s := range env.Spec.EnvFromSecrets {
			if s == name {
				return true
			}
		}
		return false
	}
	// The clone must mount its OWN label-consistent managed secret
	// (acme-web-staging-secrets), NOT the source's acme-web-secrets — the
	// label-derived name is what RefreshEnvSecrets recomputes, so mounting
	// the source name would be dropped on the next addon churn.
	wantSvc := kube.ServiceSecretName("acme", "web-staging")
	if !has(wantSvc) {
		t.Errorf("cloned env missing label-consistent service secret %q: %v", wantSvc, env.Spec.EnvFromSecrets)
	}
	if has(kube.ServiceSecretName("acme", "web")) {
		t.Errorf("cloned env mounts the SOURCE secret %q (label-mismatched, will be dropped by RefreshEnvSecrets): %v",
			kube.ServiceSecretName("acme", "web"), env.Spec.EnvFromSecrets)
	}
	// And the source secret's contents must have been COPIED into the
	// clone's own secret (isolated staging config).
	copied, err := s.Kube.Clientset.CoreV1().Secrets("kuso").Get(context.Background(), wantSvc, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("clone secret %s not created: %v", wantSvc, err)
	}
	if string(copied.Data["APP_KEY"]) != "s3cr3t" {
		t.Errorf("clone secret did not copy source data: %v", copied.Data)
	}
}
