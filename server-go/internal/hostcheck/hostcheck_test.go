package hostcheck

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

func certObj(ns, name string, dnsNames []string, notAfter string, ready string, reason, msg string, transition time.Time) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("cert-manager.io/v1")
	u.SetKind("Certificate")
	u.SetNamespace(ns)
	u.SetName(name)
	names := make([]any, 0, len(dnsNames))
	for _, n := range dnsNames {
		names = append(names, n)
	}
	u.Object["spec"] = map[string]any{"dnsNames": names, "secretName": name}
	status := map[string]any{}
	if notAfter != "" {
		status["notAfter"] = notAfter
	}
	if ready != "" {
		status["conditions"] = []any{map[string]any{
			"type": "Ready", "status": ready, "reason": reason, "message": msg,
			"lastTransitionTime": transition.UTC().Format(time.RFC3339),
		}}
	}
	u.Object["status"] = status
	return u
}

func TestListCertificatesParsesStatus(t *testing.T) {
	t.Parallel()
	trans := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{GVRCertificates: "CertificateList"},
		certObj("kuso", "web-tls", []string{"web.example.com", "example.com"}, "2026-10-05T00:00:00Z", "True", "Ready", "ok", trans),
		certObj("proj-ns", "api-tls", []string{"api.example.com"}, "", "False", "Failed", "ACME challenge failed", trans),
	)
	certs, err := ListCertificates(context.Background(), dyn)
	if err != nil {
		t.Fatalf("ListCertificates: %v", err)
	}
	if len(certs) != 2 {
		t.Fatalf("got %d certs, want 2", len(certs))
	}
	byName := map[string]Cert{}
	for _, c := range certs {
		byName[c.Name] = c
	}
	web := byName["web-tls"]
	if !web.Ready || web.NotAfter == nil || !web.NotAfter.Equal(time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("web cert = %+v", web)
	}
	if strings.Join(web.DNSNames, ",") != "web.example.com,example.com" {
		t.Errorf("web dnsNames = %v", web.DNSNames)
	}
	api := byName["api-tls"]
	if api.Ready || api.Reason != "Failed" || api.Message != "ACME challenge failed" || api.Namespace != "proj-ns" {
		t.Errorf("api cert = %+v", api)
	}
	if !api.ReadyTransition.Equal(trans) {
		t.Errorf("api transition = %v, want %v", api.ReadyTransition, trans)
	}
}

func TestCertProblem(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	in := func(d time.Duration) *time.Time { v := now.Add(d); return &v }
	cases := []struct {
		name    string
		cert    Cert
		days    int
		wantBad bool
		wantSub string
	}{
		{"healthy far expiry", Cert{Ready: true, NotAfter: in(60 * 24 * time.Hour)}, 14, false, ""},
		{"expiring inside window", Cert{Ready: true, NotAfter: in(5 * 24 * time.Hour)}, 14, true, "expires in 5d"},
		{"exactly at window edge is fine", Cert{Ready: true, NotAfter: in(14*24*time.Hour + time.Minute)}, 14, false, ""},
		{"already expired", Cert{Ready: true, NotAfter: in(-2 * time.Hour)}, 14, true, "expired"},
		{"not ready past grace", Cert{Ready: false, Reason: "Failed", Message: "ACME challenge failed", ReadyTransition: now.Add(-time.Hour)}, 14, true, "not Ready (Failed: ACME challenge failed)"},
		{"not ready inside issuance grace", Cert{Ready: false, Reason: "Issuing", ReadyTransition: now.Add(-2 * time.Minute)}, 14, false, ""},
		{"not ready with no transition uses creation", Cert{Ready: false, Reason: "DoesNotExist", Created: now.Add(-time.Hour)}, 14, true, "not Ready"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, bad := CertProblem(tc.cert, now, tc.days)
			if bad != tc.wantBad {
				t.Fatalf("bad = %v (%q), want %v", bad, got, tc.wantBad)
			}
			if tc.wantBad && !strings.Contains(got, tc.wantSub) {
				t.Errorf("problem = %q, want it to contain %q", got, tc.wantSub)
			}
		})
	}
}

func TestCheckable(t *testing.T) {
	t.Parallel()
	for host, want := range map[string]bool{
		"web.example.com":       true,
		"example.com":           true,
		"*.example.com":         false,
		"api.svc.cluster.local": false,
		"thing.internal":        false,
		"localhost":             false,
		"proj":                  false,
		"":                      false,
		"com":                   false,
		"app.localtest.me":      true,
		"UPPER.Example.COM":     true,
	} {
		if got := Checkable(host); got != want {
			t.Errorf("Checkable(%q) = %v, want %v", host, got, want)
		}
	}
}

type fakeResolver map[string][]string

func (f fakeResolver) LookupHost(ctx context.Context, host string) ([]string, error) {
	if v, ok := f[host]; ok {
		return v, nil
	}
	return nil, errors.New("no such host")
}

func TestCheckHosts(t *testing.T) {
	t.Parallel()
	r := fakeResolver{
		"ok.example.com":    {"203.0.113.10"},
		"multi.example.com": {"198.51.100.7", "203.0.113.11"},
		"wrong.example.com": {"198.51.100.7"},
		"cf.example.com":    {"104.16.1.1", "2606:4700::6810:101"},
	}
	res := CheckHosts(context.Background(), r,
		[]string{"ok.example.com", "multi.example.com", "wrong.example.com", "gone.example.com", "cf.example.com"},
		[]string{"203.0.113.10", "203.0.113.11"}, time.Second)
	got := map[string]DNSStatus{}
	for _, x := range res {
		got[x.Host] = x.Status
	}
	want := map[string]DNSStatus{
		"ok.example.com":    DNSOK,
		"multi.example.com": DNSOK,
		"wrong.example.com": DNSMismatch,
		"gone.example.com":  DNSUnresolved,
		"cf.example.com":    DNSProxied,
	}
	for h, w := range want {
		if got[h] != w {
			t.Errorf("%s: status %q, want %q", h, got[h], w)
		}
	}
}

// A resolver that blocks must not hang the check past its timeout.
type slowResolver struct{}

func (slowResolver) LookupHost(ctx context.Context, host string) ([]string, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestCheckHostsTimeout(t *testing.T) {
	t.Parallel()
	start := time.Now()
	res := CheckHosts(context.Background(), slowResolver{}, []string{"a.example.com", "b.example.com"}, []string{"1.2.3.4"}, 50*time.Millisecond)
	if time.Since(start) > 2*time.Second {
		t.Fatalf("CheckHosts ignored its timeout")
	}
	for _, r := range res {
		if r.Status != DNSUnresolved {
			t.Errorf("%s: status %q, want unresolved on timeout", r.Host, r.Status)
		}
	}
}

func TestExpectedIngressIPs(t *testing.T) {
	t.Parallel()
	cs := kubefake.NewSimpleClientset(
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "traefik", Namespace: "traefik"},
			Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{
				Ingress: []corev1.LoadBalancerIngress{{IP: "203.0.113.10"}},
			}},
		},
		&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "n1"},
			Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{
				{Type: corev1.NodeInternalIP, Address: "203.0.113.11"},
				{Type: corev1.NodeExternalIP, Address: "198.51.100.20"},
				{Type: corev1.NodeHostName, Address: "n1"},
			}},
		},
	)
	ips, err := ExpectedIngressIPs(context.Background(), cs, "")
	if err != nil {
		t.Fatalf("ExpectedIngressIPs: %v", err)
	}
	got := strings.Join(ips, ",")
	for _, want := range []string{"203.0.113.10", "203.0.113.11", "198.51.100.20"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected IPs %q missing %s", got, want)
		}
	}
	if strings.Contains(got, "n1") {
		t.Errorf("hostname leaked into IP set: %q", got)
	}

	// An explicit override wins outright — Cloudflare tunnels / external
	// LBs have addresses the cluster can't see.
	ips, err = ExpectedIngressIPs(context.Background(), cs, " 192.0.2.1, 192.0.2.2 ")
	if err != nil {
		t.Fatalf("override: %v", err)
	}
	if strings.Join(ips, ",") != "192.0.2.1,192.0.2.2" {
		t.Errorf("override ips = %v", ips)
	}
}
