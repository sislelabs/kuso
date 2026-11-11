package uptime

import (
	"context"
	"testing"

	"kuso/server/internal/notify"
)

func countEvents(f *fakeNotify, typ notify.EventType) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, e := range f.events {
		if e.Type == typ {
			n++
		}
	}
	return n
}

// newIngressHarness: two services with public hosts that always answer
// in-cluster; ingressOK says whether Traefik answers.
func newIngressHarness(ingressOK *bool) *harness {
	a, b := tgt("shop", "web"), tgt("blog", "web")
	a.Host, b.Host = "shop.example.com", "blog.example.com"
	h := newHarness(a, b)
	h.w.IngressBase = func(context.Context) string { return "http://traefik.traefik.svc.cluster.local" }
	h.w.IngressProbe = func(_ context.Context, url, host string) Result {
		if *ingressOK {
			return Result{OK: true, StatusCode: 404}
		}
		return Result{Error: "connection refused"}
	}
	return h
}

// Traefik dead while the apps answer in-cluster pages once, only after
// the debounce (never on the first tick), and recovers once.
func TestIngressDownPagesOnceAfterDebounce(t *testing.T) {
	ok := false
	h := newIngressHarness(&ok)
	h.ticks(ingressFailTicks - 1)
	if n := countEvents(h.notify, notify.EventIngressDown); n != 0 {
		t.Fatalf("paged before %d failed ticks: %d", ingressFailTicks, n)
	}
	h.ticks(5)
	if n := countEvents(h.notify, notify.EventIngressDown); n != 1 {
		t.Fatalf("ingress.down = %d, want 1", n)
	}
	if n := countEvents(h.notify, notify.EventUptimeDown); n != 0 {
		t.Fatalf("per-service uptime.down fired for an ingress outage: %d", n)
	}
	ok = true
	h.ticks(ingressOKTicks + 2)
	if n := countEvents(h.notify, notify.EventPlatformRecovered); n != 1 {
		t.Fatalf("platform.recovered = %d, want 1", n)
	}
}

// One host failing through Traefik (its own route is broken) is not an
// ingress-wide outage.
func TestIngressPartialFailureIsNotDown(t *testing.T) {
	ok := true
	h := newIngressHarness(&ok)
	h.w.IngressProbe = func(_ context.Context, _, host string) Result {
		if host == "blog.example.com" {
			return Result{Error: "HTTP 502"}
		}
		return Result{OK: true}
	}
	h.ticks(6)
	if n := countEvents(h.notify, notify.EventIngressDown); n != 0 {
		t.Fatalf("ingress.down = %d on a single-host failure, want 0", n)
	}
}

// The opt-in public-path probe catches what the ClusterIP probe can't
// (broken LB/firewall/DNS while Traefik answers in-cluster).
func TestExternalPathDownWhileTraefikHealthy(t *testing.T) {
	ok := true
	h := newIngressHarness(&ok)
	extOK := false
	h.w.ExternalTarget = func() string { return "https://shop.example.com/" }
	h.w.ExternalProbe = func(context.Context, string) Result {
		if extOK {
			return Result{OK: true}
		}
		return Result{Error: "i/o timeout"}
	}
	h.ticks(ingressFailTicks - 1)
	if n := countEvents(h.notify, notify.EventIngressDown); n != 0 {
		t.Fatalf("paged before the debounce: %d", n)
	}
	h.ticks(4)
	if n := countEvents(h.notify, notify.EventIngressDown); n != 1 {
		t.Fatalf("ingress.down = %d, want 1", n)
	}
	extOK = true
	h.ticks(ingressOKTicks)
	if n := countEvents(h.notify, notify.EventPlatformRecovered); n != 1 {
		t.Fatalf("platform.recovered = %d, want 1", n)
	}
}

func TestIngressURLKeepsPathAndQuery(t *testing.T) {
	got := ingressURL("http://traefik.traefik.svc.cluster.local", "http://web.kuso.svc.cluster.local/healthz?x=1")
	if got != "http://traefik.traefik.svc.cluster.local/healthz?x=1" {
		t.Fatalf("ingressURL = %q", got)
	}
}
