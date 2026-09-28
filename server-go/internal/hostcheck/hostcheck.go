// Package hostcheck inspects the public edge of an env's hostnames: the
// cert-manager Certificates that serve them and whether their DNS
// actually points at this cluster. Both failure modes are silent from
// inside kuso — a cert that stops renewing or a domain whose A record
// was moved keeps every pod green while users get TLS errors or someone
// else's server. The alerts engine turns these checks into rules.
package hostcheck

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"golang.org/x/net/publicsuffix"
)

// GVRCertificates is cert-manager's Certificate resource. ingress-shim
// creates one per Ingress tls secret, so every TLS host kuso renders is
// covered by exactly one of these.
var GVRCertificates = schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}

// Cert is the slice of a cert-manager Certificate the alert rules need.
type Cert struct {
	Namespace string
	Name      string
	DNSNames  []string
	// NotAfter is nil until the first issuance succeeds.
	NotAfter *time.Time
	Ready    bool
	Reason   string
	Message  string
	// ReadyTransition is the Ready condition's lastTransitionTime; zero
	// when cert-manager hasn't written the condition yet.
	ReadyTransition time.Time
	Created         time.Time
}

// ListCertificates lists every Certificate in the cluster.
func ListCertificates(ctx context.Context, dyn dynamic.Interface) ([]Cert, error) {
	list, err := dyn.Resource(GVRCertificates).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list certificates: %w", err)
	}
	out := make([]Cert, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, parseCert(&list.Items[i]))
	}
	return out, nil
}

func parseCert(u *unstructured.Unstructured) Cert {
	c := Cert{Namespace: u.GetNamespace(), Name: u.GetName(), Created: u.GetCreationTimestamp().Time}
	c.DNSNames, _, _ = unstructured.NestedStringSlice(u.Object, "spec", "dnsNames")
	if s, _, _ := unstructured.NestedString(u.Object, "status", "notAfter"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			t = t.UTC()
			c.NotAfter = &t
		}
	}
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, raw := range conds {
		m, ok := raw.(map[string]any)
		if !ok || m["type"] != "Ready" {
			continue
		}
		c.Ready = m["status"] == "True"
		c.Reason, _ = m["reason"].(string)
		c.Message, _ = m["message"].(string)
		if s, ok := m["lastTransitionTime"].(string); ok {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				c.ReadyTransition = t.UTC()
			}
		}
	}
	return c
}

// notReadyGrace is how long a Certificate may sit not-Ready before it
// counts as a problem. A fresh cert (new domain, renewal in flight) is
// legitimately not-Ready while the ACME challenge runs; LE normally
// finishes in under a minute, so 15 minutes only swallows real churn.
const notReadyGrace = 15 * time.Minute

// CertProblem reports what's wrong with c, if anything: not Ready past
// the issuance grace, already expired, or expiring within days.
func CertProblem(c Cert, now time.Time, days int) (string, bool) {
	if !c.Ready {
		since := c.ReadyTransition
		if since.IsZero() {
			since = c.Created
		}
		if since.IsZero() || now.Sub(since) >= notReadyGrace {
			p := "not Ready"
			switch {
			case c.Reason != "" && c.Message != "":
				p += " (" + c.Reason + ": " + c.Message + ")"
			case c.Reason != "":
				p += " (" + c.Reason + ")"
			}
			return p, true
		}
	}
	if c.NotAfter == nil {
		return "", false
	}
	left := c.NotAfter.Sub(now)
	if left <= 0 {
		return "expired " + c.NotAfter.Format("2006-01-02"), true
	}
	if left < time.Duration(days)*24*time.Hour {
		return fmt.Sprintf("expires in %dd (%s)", int(left.Hours()/24), c.NotAfter.Format("2006-01-02")), true
	}
	return "", false
}

// internalSuffixes are names that never resolve publicly, so a DNS
// check against them is always a false alarm.
var internalSuffixes = []string{".local", ".internal", ".localhost", ".svc", ".lan", ".home.arpa", ".test", ".invalid"}

// Checkable reports whether host is a public FQDN worth resolving:
// not a wildcard, not cluster-internal, and under a real ICANN suffix.
func Checkable(host string) bool {
	h := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if h == "" || strings.Contains(h, "*") || !strings.Contains(h, ".") {
		return false
	}
	for _, s := range internalSuffixes {
		if strings.HasSuffix(h, s) {
			return false
		}
	}
	suffix, icann := publicsuffix.PublicSuffix(h)
	return icann && h != suffix
}

// Resolver is the slice of *net.Resolver the DNS check uses.
type Resolver interface {
	LookupHost(ctx context.Context, host string) ([]string, error)
}

type DNSStatus string

const (
	DNSOK DNSStatus = "ok"
	// DNSMismatch: the host resolves, but to none of the cluster's IPs.
	DNSMismatch DNSStatus = "mismatch"
	// DNSUnresolved: lookup failed (NXDOMAIN, timeout, no records).
	DNSUnresolved DNSStatus = "unresolved"
	// DNSProxied: every address is Cloudflare's edge, so the origin
	// behind it can't be verified from DNS. Not an error.
	DNSProxied DNSStatus = "proxied"
)

type DNSResult struct {
	Host     string
	Status   DNSStatus
	Resolved []string
	Err      error
}

const dnsWorkers = 8

// CheckHosts resolves each host (bounded concurrency, per-host timeout)
// and classifies it against the expected ingress IPs. Results keep the
// input order.
func CheckHosts(ctx context.Context, r Resolver, hosts, expected []string, timeout time.Duration) []DNSResult {
	want := make(map[string]struct{}, len(expected))
	for _, ip := range expected {
		if p := net.ParseIP(strings.TrimSpace(ip)); p != nil {
			want[p.String()] = struct{}{}
		}
	}
	out := make([]DNSResult, len(hosts))
	sem := make(chan struct{}, dnsWorkers)
	var wg sync.WaitGroup
	for i, h := range hosts {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, h string) {
			defer wg.Done()
			defer func() { <-sem }()
			lctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			addrs, err := r.LookupHost(lctx, h)
			out[i] = classify(h, addrs, err, want)
		}(i, h)
	}
	wg.Wait()
	return out
}

func classify(host string, addrs []string, err error, want map[string]struct{}) DNSResult {
	res := DNSResult{Host: host, Resolved: addrs, Err: err}
	if err != nil || len(addrs) == 0 {
		res.Status = DNSUnresolved
		return res
	}
	allCF := true
	for _, a := range addrs {
		ip := net.ParseIP(a)
		if ip == nil {
			allCF = false
			continue
		}
		if _, ok := want[ip.String()]; ok {
			res.Status = DNSOK
			return res
		}
		if !isCloudflare(ip) {
			allCF = false
		}
	}
	if allCF {
		res.Status = DNSProxied
	} else {
		res.Status = DNSMismatch
	}
	return res
}

// cloudflareRanges is https://www.cloudflare.com/ips/. Stable for years;
// a stale entry only turns a proxied host into a (visible) mismatch.
var cloudflareRanges = func() []*net.IPNet {
	cidrs := []string{
		"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22",
		"141.101.64.0/18", "108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20",
		"197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
		"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
		"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32",
		"2405:8100::/32", "2a06:98c0::/29", "2c0f:f248::/32",
	}
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err == nil {
			out = append(out, n)
		}
	}
	return out
}()

func isCloudflare(ip net.IP) bool {
	for _, n := range cloudflareRanges {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// traefikServices are where the k3s / kuso installs put the ingress
// controller's LoadBalancer Service.
var traefikServices = []struct{ ns, name string }{{"traefik", "traefik"}, {"kube-system", "traefik"}}

// ExpectedIngressIPs is the set of addresses a correctly-pointed host may
// resolve to: the traefik LoadBalancer ingress IPs plus every node's
// Internal/External IP (k3s servicelb binds the host ports on each node).
// override (KUSO_INGRESS_IPS, comma-separated) replaces the discovery
// for setups the cluster can't see, like an external load balancer.
func ExpectedIngressIPs(ctx context.Context, cs kubernetes.Interface, override string) ([]string, error) {
	if strings.TrimSpace(override) != "" {
		var out []string
		for _, p := range strings.Split(override, ",") {
			if ip := net.ParseIP(strings.TrimSpace(p)); ip != nil {
				out = append(out, ip.String())
			}
		}
		return out, nil
	}
	set := map[string]struct{}{}
	for _, ts := range traefikServices {
		svc, err := cs.CoreV1().Services(ts.ns).Get(ctx, ts.name, metav1.GetOptions{})
		if err != nil {
			continue
		}
		for _, ing := range svc.Status.LoadBalancer.Ingress {
			if ip := net.ParseIP(ing.IP); ip != nil {
				set[ip.String()] = struct{}{}
			}
		}
	}
	nodes, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	for i := range nodes.Items {
		for _, a := range nodes.Items[i].Status.Addresses {
			if a.Type != corev1.NodeInternalIP && a.Type != corev1.NodeExternalIP {
				continue
			}
			if ip := net.ParseIP(a.Address); ip != nil {
				set[ip.String()] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(set))
	for ip := range set {
		out = append(out, ip)
	}
	sort.Strings(out)
	return out, nil
}
