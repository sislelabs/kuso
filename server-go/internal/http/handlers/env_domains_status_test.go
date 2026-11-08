package handlers

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuso/server/internal/hostcheck"
	"kuso/server/internal/kube"
)

func TestEnvDomainsOf(t *testing.T) {
	t.Parallel()
	env := &kube.KusoEnvironment{}
	env.Spec.Host = "Web.Example.com"
	env.Spec.AdditionalHosts = []string{"api.example.com", "web.example.com", " "}
	env.Spec.WildcardDomains = []kube.KusoWildcardDomain{{Host: "*.example.com"}}

	want := []envDomain{
		{"web.example.com", domainKindAuto},
		{"api.example.com", domainKindCustom},
		{"*.example.com", domainKindWildcard},
	}
	if got := envDomainsOf(env); !reflect.DeepEqual(got, want) {
		t.Fatalf("envDomainsOf = %+v, want %+v", got, want)
	}
}

func TestTLSFor(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	future := now.Add(60 * 24 * time.Hour)
	past := now.Add(-time.Hour)
	host := envDomain{"app.example.com", domainKindCustom}
	cert := func(mod func(*hostcheck.Cert)) []hostcheck.Cert {
		c := hostcheck.Cert{DNSNames: []string{"app.example.com"}, Created: now.Add(-time.Hour)}
		mod(&c)
		return []hostcheck.Cert{c}
	}

	cases := []struct {
		name   string
		d      envDomain
		certs  []hostcheck.Cert
		status tlsStatus
	}{
		{"wildcard is external", envDomain{"*.example.com", domainKindWildcard}, nil, tlsExternal},
		{"no cert", host, cert(func(c *hostcheck.Cert) { c.DNSNames = []string{"other.example.com"} }), tlsMissing},
		{"ready", host, cert(func(c *hostcheck.Cert) { c.Ready = true; c.NotAfter = &future }), tlsReady},
		{"ready but expired", host, cert(func(c *hostcheck.Cert) { c.Ready = true; c.NotAfter = &past }), tlsFailed},
		{"not ready within grace", host, cert(func(c *hostcheck.Cert) { c.ReadyTransition = now.Add(-time.Minute) }), tlsIssuing},
		{"not ready past grace", host, cert(func(c *hostcheck.Cert) {
			c.ReadyTransition = now.Add(-time.Hour)
			c.Reason, c.Message = "Failed", "acme challenge failed"
		}), tlsFailed},
		{"ready cert wins over stale one", host, append(
			cert(func(c *hostcheck.Cert) { c.ReadyTransition = now.Add(-time.Hour) }),
			cert(func(c *hostcheck.Cert) { c.Ready = true; c.NotAfter = &future })...), tlsReady},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tlsFor(tc.d, tc.certs, now)
			if got.Status != tc.status {
				t.Fatalf("status = %q, want %q (%+v)", got.Status, tc.status, got)
			}
			if tc.name == "not ready past grace" && (got.Reason != "Failed" || got.Message != "acme challenge failed") {
				t.Fatalf("reason/message not passed through: %+v", got)
			}
		})
	}
}

func TestDiagnoseTLS(t *testing.T) {
	t.Parallel()
	failed := func(reason, failure string) domainTLS {
		return domainTLS{Status: tlsFailed, Reason: reason, failure: failure}
	}
	cases := []struct {
		name string
		tls  domainTLS
		dns  hostcheck.DNSStatus
		noCM bool
		want tlsCause
	}{
		{"ready has no cause", domainTLS{Status: tlsReady}, hostcheck.DNSOK, false, ""},
		{"external has no cause", domainTLS{Status: tlsExternal}, dnsSkipped, false, ""},
		{"unknown has no cause", domainTLS{Status: tlsUnknown}, hostcheck.DNSOK, false, ""},
		{"cert-manager missing", domainTLS{Status: tlsMissing}, hostcheck.DNSOK, true, causeCertManagerMissing},
		{"rate limit beats bad DNS", failed("Failed", "Failed to create Order: 429 urn:ietf:params:acme:error:rateLimited: too many certificates already issued"), hostcheck.DNSMismatch, false, causeRateLimited},
		{"caa", failed("Failed", "urn:ietf:params:acme:error:caa: CAA record for example.com prevents issuance"), hostcheck.DNSOK, false, causeCAA},
		{"mismatch while issuing", domainTLS{Status: tlsIssuing}, hostcheck.DNSMismatch, false, causeDNSNotPointed},
		{"unresolved while missing", domainTLS{Status: tlsMissing}, hostcheck.DNSUnresolved, false, causeDNSNotPointed},
		{"proxied and failed", failed("Failed", "order is in invalid state"), hostcheck.DNSProxied, false, causeCloudflareProxy},
		{"expired", failed("Expired", ""), hostcheck.DNSOK, false, causeExpired},
		{"generic failure", failed("Failed", "order is in invalid state"), hostcheck.DNSOK, false, causeValidationFailed},
		{"issuing", domainTLS{Status: tlsIssuing}, hostcheck.DNSOK, false, causeIssuing},
		{"issuing, DNS unchecked", domainTLS{Status: tlsIssuing}, dnsSkipped, false, causeIssuing},
		{"not requested", domainTLS{Status: tlsMissing}, hostcheck.DNSOK, false, causeNotRequested},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := diagnoseTLS(tc.tls, tc.dns, tc.noCM); got != tc.want {
				t.Fatalf("cause = %q, want %q", got, tc.want)
			}
		})
	}
}

type stubResolver map[string][]string

func (r stubResolver) LookupHost(_ context.Context, host string) ([]string, error) {
	if a, ok := r[host]; ok {
		return a, nil
	}
	return nil, errors.New("no such host")
}

func domainStatusEnv() *kube.KusoEnvironment {
	env := &kube.KusoEnvironment{}
	env.Namespace = "kuso-shop"
	env.Spec.Host = "web.shop.example.net"
	env.Spec.AdditionalHosts = []string{"app.example.com", "new.example.com"}
	return env
}

func ingressClientset() *kubefake.Clientset {
	return kubefake.NewSimpleClientset(&corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "traefik", Namespace: "traefik"},
		Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{
			Ingress: []corev1.LoadBalancerIngress{{IP: "203.0.113.10"}},
		}},
	})
}

func certificate(name string, dnsNames []string, conds []any, status map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("cert-manager.io/v1")
	u.SetKind("Certificate")
	u.SetNamespace("kuso-shop")
	u.SetName(name)
	names := make([]any, 0, len(dnsNames))
	for _, n := range dnsNames {
		names = append(names, n)
	}
	u.Object["spec"] = map[string]any{"dnsNames": names}
	if status == nil {
		status = map[string]any{}
	}
	status["conditions"] = conds
	u.Object["status"] = status
	return u
}

func TestBuildDomainsStatusDiagnoses(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	recent := now.Add(-2 * time.Minute).Format(time.RFC3339)
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{hostcheck.GVRCertificates: "CertificateList"},
		certificate("web-tls", []string{"web.shop.example.net"}, []any{
			map[string]any{"type": "Ready", "status": "True", "reason": "Ready"},
		}, map[string]any{"notAfter": now.Add(60 * 24 * time.Hour).Format(time.RFC3339)}),
		certificate("app-tls", []string{"app.example.com"}, []any{
			map[string]any{"type": "Ready", "status": "False", "reason": "DoesNotExist", "lastTransitionTime": recent},
		}, nil),
		certificate("new-tls", []string{"new.example.com"}, []any{
			map[string]any{"type": "Ready", "status": "False", "reason": "DoesNotExist", "lastTransitionTime": recent},
			map[string]any{"type": "Issuing", "status": "False", "reason": "Failed",
				"message": "Failed to create Order: 429 urn:ietf:params:acme:error:rateLimited: too many certificates"},
		}, map[string]any{"failedIssuanceAttempts": int64(2), "lastFailureTime": recent}),
	)
	h := &ProjectsHandler{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Kube:   &kube.Client{Clientset: ingressClientset(), Dynamic: dyn},
	}
	resolver := stubResolver{
		"web.shop.example.net": {"203.0.113.10"},
		"app.example.com":      {"198.51.100.7"},
		"new.example.com":      {"203.0.113.10"},
	}
	resp := h.buildDomainsStatus(context.Background(), domainStatusEnv(), now, resolver)

	if !reflect.DeepEqual(resp.ExpectedIPs, []string{"203.0.113.10"}) {
		t.Fatalf("expectedIPs = %v", resp.ExpectedIPs)
	}
	by := map[string]domainStatus{}
	for _, d := range resp.Domains {
		by[d.Host] = d
	}
	if d := by["web.shop.example.net"]; d.TLS.Status != tlsReady || d.TLS.Cause != "" || d.DNS.Status != hostcheck.DNSOK {
		t.Errorf("web = %+v", d)
	}
	app := by["app.example.com"]
	if app.DNS.Status != hostcheck.DNSMismatch || !reflect.DeepEqual(app.DNS.Resolved, []string{"198.51.100.7"}) {
		t.Errorf("app dns = %+v", app.DNS)
	}
	if app.TLS.Status != tlsIssuing || app.TLS.Cause != causeDNSNotPointed || app.TLS.Since != recent {
		t.Errorf("app tls = %+v", app.TLS)
	}
	nw := by["new.example.com"]
	if nw.TLS.Cause != causeRateLimited || nw.TLS.Attempts != 2 || nw.TLS.LastFailure != recent {
		t.Errorf("new tls = %+v", nw.TLS)
	}
}

func TestBuildDomainsStatusWithoutCertManager(t *testing.T) {
	t.Parallel()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{hostcheck.GVRCertificates: "CertificateList"})
	dyn.PrependReactor("list", "certificates", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(hostcheck.GVRCertificates.GroupResource(), "")
	})
	h := &ProjectsHandler{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Kube:   &kube.Client{Clientset: ingressClientset(), Dynamic: dyn},
	}
	resolver := stubResolver{"web.shop.example.net": {"203.0.113.10"}, "app.example.com": {"203.0.113.10"}}
	resp := h.buildDomainsStatus(context.Background(), domainStatusEnv(), time.Now(), resolver)
	for _, d := range resp.Domains {
		if d.Host == "new.example.com" {
			// Unresolved, but a missing cert-manager is the bigger problem.
			if d.DNS.Status != hostcheck.DNSUnresolved {
				t.Errorf("new dns = %+v", d.DNS)
			}
		}
		if d.TLS.Status != tlsMissing || d.TLS.Cause != causeCertManagerMissing {
			t.Errorf("%s tls = %+v, want missing/cert_manager_missing", d.Host, d.TLS)
		}
	}
}
