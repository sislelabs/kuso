package handlers

import (
	"context"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// IngressTargets is the body of GET /api/config/ingress: where a custom
// domain's DNS record should point. The CLI and web read this exact shape.
type IngressTargets struct {
	IPs       []string `json:"ips"`
	Hostnames []string `json:"hostnames"`
	// Source is "loadbalancer" (ingress controller Service status),
	// "nodes" (Ready nodes' addresses) or "none".
	Source string `json:"source"`
}

// ingressServiceLocations are where kuso's install (namespace traefik) and a
// stock k3s install (kube-system) put the Traefik LoadBalancer Service.
var ingressServiceLocations = []struct{ ns, name string }{{"traefik", "traefik"}, {"kube-system", "traefik"}}

const ingressCacheTTL = 60 * time.Second

type ingressCache struct {
	mu      sync.Mutex
	val     IngressTargets
	expires time.Time
}

// deriveIngressTargets picks the DNS target from the ingress controller's
// LoadBalancer status, falling back to Ready nodes' ExternalIPs, then their
// InternalIPs when no node has an ExternalIP. Private (RFC 1918 / ULA) IPs
// are dropped whenever a public one is available.
func deriveIngressTargets(svcs []corev1.Service, nodes []corev1.Node) IngressTargets {
	var lbIPs, lbHosts []string
	for i := range svcs {
		for _, ing := range svcs[i].Status.LoadBalancer.Ingress {
			if ip := net.ParseIP(ing.IP); ip != nil {
				lbIPs = append(lbIPs, ip.String())
			}
			if ing.Hostname != "" {
				lbHosts = append(lbHosts, ing.Hostname)
			}
		}
	}
	if len(lbIPs) > 0 || len(lbHosts) > 0 {
		return IngressTargets{IPs: preferPublic(lbIPs), Hostnames: dedupeSorted(lbHosts), Source: "loadbalancer"}
	}

	var external, internal []string
	for i := range nodes {
		if !nodeReady(&nodes[i]) {
			continue
		}
		for _, a := range nodes[i].Status.Addresses {
			ip := net.ParseIP(a.Address)
			if ip == nil {
				continue
			}
			switch a.Type {
			case corev1.NodeExternalIP:
				external = append(external, ip.String())
			case corev1.NodeInternalIP:
				internal = append(internal, ip.String())
			}
		}
	}
	ips := external
	if len(ips) == 0 {
		ips = internal
	}
	if len(ips) > 0 {
		return IngressTargets{IPs: preferPublic(ips), Hostnames: []string{}, Source: "nodes"}
	}
	return IngressTargets{IPs: []string{}, Hostnames: []string{}, Source: "none"}
}

func nodeReady(n *corev1.Node) bool {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// preferPublic returns the public IPs when there are any, else all of them.
func preferPublic(ips []string) []string {
	var public []string
	for _, s := range ips {
		ip := net.ParseIP(s)
		if ip != nil && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
			public = append(public, s)
		}
	}
	if len(public) > 0 {
		return dedupeSorted(public)
	}
	return dedupeSorted(ips)
}

func dedupeSorted(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// IngressTargets is GET /api/config/ingress. Any authenticated user may read
// it; the answer is cached for a minute. Lookup failures answer
// source "none" rather than an error, since the caller only uses it as a hint.
func (h *KubernetesHandler) IngressTargets(w http.ResponseWriter, r *http.Request) {
	h.ingress.mu.Lock()
	defer h.ingress.mu.Unlock()
	if time.Now().Before(h.ingress.expires) {
		writeJSON(w, http.StatusOK, h.ingress.val)
		return
	}
	ctx, cancel := kubeCtx(r)
	defer cancel()
	val, err := h.lookupIngressTargets(ctx)
	if err != nil {
		h.Logger.Warn("ingress targets lookup", "err", err)
		writeJSON(w, http.StatusOK, IngressTargets{IPs: []string{}, Hostnames: []string{}, Source: "none"})
		return
	}
	h.ingress.val = val
	h.ingress.expires = time.Now().Add(ingressCacheTTL)
	writeJSON(w, http.StatusOK, val)
}

func (h *KubernetesHandler) lookupIngressTargets(ctx context.Context) (IngressTargets, error) {
	if h.Kube == nil {
		return deriveIngressTargets(nil, nil), nil
	}
	cs := h.Kube.Clientset
	var svcs []corev1.Service
	for _, loc := range ingressServiceLocations {
		svc, err := cs.CoreV1().Services(loc.ns).Get(ctx, loc.name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return IngressTargets{}, err
		}
		svcs = append(svcs, *svc)
	}
	if len(svcs) == 0 {
		// Traefik installed under another name or namespace.
		list, err := cs.CoreV1().Services("").List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=traefik"})
		if err != nil {
			return IngressTargets{}, err
		}
		for i := range list.Items {
			if list.Items[i].Spec.Type == corev1.ServiceTypeLoadBalancer {
				svcs = append(svcs, list.Items[i])
			}
		}
	}
	t := deriveIngressTargets(svcs, nil)
	if t.Source == "loadbalancer" {
		return t, nil
	}
	nodes, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return IngressTargets{}, err
	}
	return deriveIngressTargets(svcs, nodes.Items), nil
}
