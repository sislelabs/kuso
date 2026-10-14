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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// defaultTrustedProxies is the proxy set used when KUSO_TRUSTED_PROXIES
// is unset: the cluster's pod CIDRs. kuso-server is reached through the
// Traefik ingress pod, so without this every client resolves to
// Traefik's pod IP and shares one rate-limit bucket. The updater never
// re-applies deploy/server-go.yaml env, so the default has to come from
// the cluster rather than the manifest.
var defaultTrustedProxies atomic.Pointer[[]*net.IPNet]

// setDefaultTrustedProxies swaps the fallback set. nil/empty = trust nothing.
func setDefaultTrustedProxies(cidrs []*net.IPNet) {
	if len(cidrs) == 0 {
		defaultTrustedProxies.Store(nil)
		return
	}
	cp := append([]*net.IPNet(nil), cidrs...)
	defaultTrustedProxies.Store(&cp)
}

// podCIDRsFromNodes unions node.Spec.PodCIDRs (falling back to the
// legacy singular PodCIDR) across every node.
func podCIDRsFromNodes(ctx context.Context, cs kubernetes.Interface) ([]*net.IPNet, error) {
	nodes, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	seen := map[string]*net.IPNet{}
	for i := range nodes.Items {
		spec := nodes.Items[i].Spec
		raw := spec.PodCIDRs
		if len(raw) == 0 && spec.PodCIDR != "" {
			raw = []string{spec.PodCIDR}
		}
		for _, c := range raw {
			_, n, perr := net.ParseCIDR(strings.TrimSpace(c))
			if perr != nil {
				continue
			}
			seen[n.String()] = n
		}
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

// StartPodCIDRTrustedProxies populates the default trusted-proxy set
// from the cluster's node pod CIDRs and refreshes it every interval
// until ctx is done, so nodes joining later are picked up. A failed
// refresh keeps the previous set. No-op when KUSO_TRUSTED_PROXIES is
// set (the explicit list always wins) or cs is nil. Uses nodes list,
// which kuso-server's ClusterRole already grants.
func StartPodCIDRTrustedProxies(ctx context.Context, cs kubernetes.Interface, logger *slog.Logger, interval time.Duration) {
	if cs == nil || strings.TrimSpace(os.Getenv("KUSO_TRUSTED_PROXIES")) != "" {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	refresh := func() {
		lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		cidrs, err := podCIDRsFromNodes(lctx, cs)
		if err != nil {
			logger.Warn("trusted proxies: list nodes for pod CIDRs", "err", err)
			return
		}
		if len(cidrs) == 0 {
			logger.Warn("trusted proxies: no node has spec.podCIDRs; X-Forwarded-For stays ignored (set KUSO_TRUSTED_PROXIES)")
		}
		setDefaultTrustedProxies(cidrs)
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
