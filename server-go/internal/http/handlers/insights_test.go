package handlers

import (
	"context"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"kuso/server/internal/hostcheck"
)

func TestFillTrafficMergesPortsAndSkipsUnknownEnvs(t *testing.T) {
	svcs := []insightsService{{Project: "a", Service: "web"}}
	byEnvKey := map[string]int{"kuso-a-web-production": 0}
	tr := insightsTraffic{
		req: map[string][][2]float64{
			"kuso-a-web-production-80@kubernetes":      {{100, 1}, {700, 2}},
			"kuso-a-web-production-9000@kubernetes":    {{100, 1}},
			"kuso-secret-api-production-80@kubernetes": {{100, 50}},
		},
		errs: map[string][][2]float64{
			"kuso-a-web-production-80@kubernetes": {{100, 0.5}},
		},
		p95: map[string][][2]float64{
			"kuso-a-web-production-80@kubernetes":   {{100, 40}},
			"kuso-a-web-production-9000@kubernetes": {{100, 90}},
		},
	}
	fillTraffic(svcs, byEnvKey, tr)
	s := svcs[0]
	if want := [][2]float64{{100, 120}, {700, 120}}; !reflect.DeepEqual(s.Requests, want) {
		t.Errorf("requests = %v, want %v", s.Requests, want)
	}
	if want := [][2]float64{{100, 25}, {700, 0}}; !reflect.DeepEqual(s.ErrorRate, want) {
		t.Errorf("errorRate = %v, want %v", s.ErrorRate, want)
	}
	if want := [][2]float64{{100, 90}}; !reflect.DeepEqual(s.P95, want) {
		t.Errorf("p95 = %v, want %v", s.P95, want)
	}
}

func TestInsightsCacheServesStaleAndRefreshesOnce(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	insightsFetch = func(ctx context.Context, rg insightsRange, now time.Time) (insightsTraffic, error) {
		n := calls.Add(1)
		if n > 1 {
			<-release
		}
		return insightsTraffic{req: map[string][][2]float64{"v": {{float64(n), 0}}}}, nil
	}
	t.Cleanup(func() {
		insightsFetch = fetchInsightsTraffic
		insightsCache.mu.Lock()
		insightsCache.m = nil
		insightsCache.mu.Unlock()
	})
	rg := insightsRanges["1h"]
	ctx := context.Background()

	if _, err := cachedInsightsTraffic(ctx, "t", rg, time.Now()); err != nil {
		t.Fatal(err)
	}
	// Stale: served at once from the first fetch while one refresh runs.
	later := time.Now().Add(5 * time.Minute)
	for range 3 {
		v, err := cachedInsightsTraffic(ctx, "t", rg, later)
		if err != nil || v.req["v"][0][0] != 1 {
			t.Fatalf("stale read = %v, %v; want the first fetch", v.req, err)
		}
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	if got := calls.Load(); got != 2 {
		t.Fatalf("fetches = %d, want 2 (one refresh for three stale reads)", got)
	}
}

func TestParseResources(t *testing.T) {
	got := parseResources(map[string]any{
		"requests": map[string]any{"cpu": "500m", "memory": "512Mi"},
		"limits":   map[string]any{"memory": "4Gi"},
	})
	want := &insightsResources{CPURequestMilli: 500, MemRequestBytes: 512 << 20, MemLimitBytes: 4 << 30}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if parseResources(nil) != nil || parseResources(map[string]any{"requests": map[string]any{}}) != nil {
		t.Fatal("unset resources should be nil")
	}
}

func TestCertProblemsOnlyForCallersHosts(t *testing.T) {
	now := time.Now()
	soon := now.Add(3 * 24 * time.Hour)
	later := now.Add(60 * 24 * time.Hour)
	certs := []hostcheck.Cert{
		{Name: "a", DNSNames: []string{"a.example.com"}, Ready: true, NotAfter: &soon},
		{Name: "b", DNSNames: []string{"secret.example.com"}, Ready: true, NotAfter: &soon},
		{Name: "c", DNSNames: []string{"ok.example.com"}, Ready: true, NotAfter: &later},
	}
	got := certProblems(certs, map[string]string{"a.example.com": "a", "ok.example.com": "a"}, now)
	if len(got) != 1 || got[0].Host != "a.example.com" || got[0].Project != "a" {
		t.Fatalf("got %+v", got)
	}
}
