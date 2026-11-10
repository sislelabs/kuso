package uptime

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"kuso/server/internal/notify"
)

// The per-service checks dial each env's Service directly, so they skip
// Traefik: during the 2026-10-08 Traefik OOM every host was down while
// every check read "up". The ingress check sends a few of this tick's
// healthy services' requests through the Traefik Service as well (same
// path, Host header set), and pages when all of them fail there while
// they answer in-cluster. It goes through the in-cluster Traefik
// Service rather than the public URL because tenant traffic to the
// public address from inside the cluster hits the hairpin problem.

const (
	// ingressSample bounds how many hosts go through Traefik per tick.
	ingressSample = 3
	// ingressFailTicks / ingressOKTicks debounce the verdict. A fresh
	// process starts from zero, so the first tick can never page.
	ingressFailTicks = 3
	ingressOKTicks   = 2
)

// traefikServiceHosts are where kuso and k3s installs put Traefik.
var traefikServiceHosts = []string{"traefik.traefik.svc.cluster.local", "traefik.kube-system.svc.cluster.local"}

// ingressState is kept in memory: losing it on a leader change only
// delays a page by ingressFailTicks.
type ingressState struct {
	fails, oks int
	down       bool
	downAt     time.Time
	lastErr    string
	base       string // resolved Traefik base URL, "" until found
	baseLogged bool
}

// ProbeHost GETs target with the Host header set to host.
func (p *Prober) ProbeHost(ctx context.Context, target, host string) Result {
	return p.do(ctx, target, host)
}

// ingressBase returns the Traefik Service base URL, or "" if none
// resolves. KUSO_UPTIME_INGRESS_URL overrides discovery.
func (w *Watcher) ingressBase(ctx context.Context) string {
	if w.IngressBase != nil {
		return w.IngressBase(ctx)
	}
	if w.ingress.base != "" {
		return w.ingress.base
	}
	if v := strings.TrimRight(os.Getenv("KUSO_UPTIME_INGRESS_URL"), "/"); v != "" {
		w.ingress.base = v
		return v
	}
	for _, h := range traefikServiceHosts {
		lctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		_, err := net.DefaultResolver.LookupHost(lctx, h)
		cancel()
		if err == nil {
			w.ingress.base = "http://" + h
			return w.ingress.base
		}
	}
	if !w.ingress.baseLogged {
		w.ingress.baseLogged = true
		w.Logger.Warn("uptime: no Traefik Service found; the through-ingress check is off", "tried", strings.Join(traefikServiceHosts, ","))
	}
	return ""
}

// ingressURL rewrites an in-cluster target URL onto the Traefik base,
// keeping its path and query.
func ingressURL(base, target string) string {
	u, err := url.Parse(target)
	if err != nil {
		return ""
	}
	b, err := url.Parse(base)
	if err != nil {
		return ""
	}
	u.Scheme, u.Host = b.Scheme, b.Host
	return u.String()
}

// sampleIngress picks up to ingressSample targets that answered
// in-cluster this tick and have a public host.
func sampleIngress(targets []Target, results map[string]Result) []Target {
	var out []Target
	for _, t := range targets {
		if t.Host == "" || t.Disabled || t.Paused != "" || t.Unknown {
			continue
		}
		if r, ok := results[t.Key()]; ok && r.OK {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	if len(out) > ingressSample {
		out = out[:ingressSample]
	}
	return out
}

// checkIngress runs the through-Traefik probes for this tick and emits
// ingress.down / platform.recovered on debounced edges.
func (w *Watcher) checkIngress(ctx context.Context, targets []Target, results map[string]Result) {
	sample := sampleIngress(targets, results)
	if len(sample) == 0 {
		return // nothing to compare against: no verdict this tick
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ProbeTimeout+5*time.Second)
	defer cancel()
	base := w.ingressBase(ctx)
	if base == "" {
		return
	}
	probe := w.IngressProbe
	if probe == nil {
		probe = NewProber(ProbeTimeout).ProbeHost
	}
	res := make([]Result, len(sample))
	var wg sync.WaitGroup
	for i, t := range sample {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res[i] = probe(ctx, ingressURL(base, t.URL), t.Host)
		}()
	}
	wg.Wait()
	allFailed := true
	var lastErr string
	var hosts []string
	for i, t := range sample {
		hosts = append(hosts, t.Host)
		if res[i].OK {
			allFailed = false
		} else {
			lastErr = t.Host + ": " + res[i].Error
		}
	}
	st := &w.ingress
	now := w.Now()
	if allFailed {
		st.oks = 0
		st.fails++
		st.lastErr = lastErr
		if st.fails >= ingressFailTicks && !st.down {
			ev := notify.IngressDown("probe through Traefik",
				fmt.Sprintf("%s answer in-cluster but not through Traefik (%d checks in a row). Last error: %s",
					strings.Join(hosts, ", "), st.fails, lastErr))
			if err := w.Notify.EmitDurable(ev); err != nil {
				w.Logger.Warn("uptime: ingress notify failed, retrying next tick", "err", err)
				return
			}
			st.down, st.downAt = true, now
		}
		return
	}
	st.fails = 0
	st.oks++
	if st.down && st.oks >= ingressOKTicks {
		if err := w.Notify.EmitDurable(notify.PlatformRecovered("ingress (Traefik)", now.Sub(st.downAt))); err != nil {
			w.Logger.Warn("uptime: ingress recovery notify failed, retrying next tick", "err", err)
			return
		}
		st.down = false
	}
}

// do is the shared GET; host "" leaves the URL's host as the Host header.
func (p *Prober) do(ctx context.Context, target, host string) Result {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return Result{Error: "bad url"}
	}
	if host != "" {
		req.Host = host
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := p.client.Do(req)
	if err != nil {
		return Result{Latency: time.Since(start), Error: p.classify(err)}
	}
	defer resp.Body.Close()
	return finishResult(resp, start)
}
