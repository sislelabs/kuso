package handlers

import (
	"context"
	"log/slog"
	"net"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// defaultTrustedProxies is the proxy set used when KUSO_TRUSTED_PROXIES
// is unset: the pod IPs behind the Traefik ingress Service. kuso-server
// is reached through those pods, so without this every client resolves
// to Traefik's pod IP and shares one rate-limit bucket. Only the ingress
// pods are trusted, not the whole pod CIDR: any other pod that can reach
// kuso-server could otherwise forge X-Forwarded-For. The updater never
// re-applies deploy/server-go.yaml env, so the default has to come from
// the cluster rather than the manifest.
var defaultTrustedProxies atomic.Pointer[[]*net.IPNet]

// ingressNamespaces are where an install puts the Traefik Service:
// hack/install.sh uses "traefik", k3s's bundled chart uses kube-system.
var ingressNamespaces = []string{"traefik", "kube-system"}

const ingressServiceName = "traefik"

// setDefaultTrustedProxies swaps the fallback set. nil/empty = trust nothing.
func setDefaultTrustedProxies(cidrs []*net.IPNet) {
	if len(cidrs) == 0 {
		defaultTrustedProxies.Store(nil)
		return
	}
	cp := append([]*net.IPNet(nil), cidrs...)
	defaultTrustedProxies.Store(&cp)
}

// ingressProxyNets returns a host-length net for every endpoint address
// of the Traefik Service's EndpointSlices.
func ingressProxyNets(ctx context.Context, cs kubernetes.Interface) ([]*net.IPNet, error) {
	seen := map[string]*net.IPNet{}
	var lastErr error
	listed := false
	for _, ns := range ingressNamespaces {
		slices, err := cs.DiscoveryV1().EndpointSlices(ns).List(ctx, metav1.ListOptions{
			LabelSelector: discoveryv1.LabelServiceName + "=" + ingressServiceName,
		})
		if err != nil {
			lastErr = err
			continue
		}
		listed = true
		for i := range slices.Items {
			for _, ep := range slices.Items[i].Endpoints {
				for _, a := range ep.Addresses {
					ip := net.ParseIP(strings.TrimSpace(a))
					if ip == nil {
						continue
					}
					bits := 128
					if ip.To4() != nil {
						ip, bits = ip.To4(), 32
					}
					n := &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}
					seen[n.String()] = n
				}
			}
		}
	}
	if !listed {
		return nil, lastErr
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]*net.IPNet, 0, len(keys))
	for _, k := range keys {
		out = append(out, seen[k])
	}
	return out, nil
}

// StartIngressTrustedProxies populates the default trusted-proxy set
// from the Traefik Service's endpoints and refreshes it every interval
// until ctx is done, so restarted ingress pods are picked up. A failed
// refresh keeps the previous set. No-op when KUSO_TRUSTED_PROXIES is
// set (the explicit list always wins) or cs is nil. Uses endpointslices
// list, which kuso-server's ClusterRole already grants.
func StartIngressTrustedProxies(ctx context.Context, cs kubernetes.Interface, logger *slog.Logger, interval time.Duration) {
	if cs == nil || strings.TrimSpace(os.Getenv("KUSO_TRUSTED_PROXIES")) != "" {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	refresh := func() {
		lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		nets, err := ingressProxyNets(lctx, cs)
		if err != nil {
			logger.Warn("trusted proxies: list ingress endpointslices", "err", err)
			return
		}
		if len(nets) == 0 {
			logger.Warn("trusted proxies: no traefik endpoints found; X-Forwarded-For stays ignored (set KUSO_TRUSTED_PROXIES)")
		}
		setDefaultTrustedProxies(nets)
	}
	go func() {
		refresh()
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				refresh()
			}
		}
	}()
}
