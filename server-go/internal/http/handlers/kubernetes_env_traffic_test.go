package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeProm answers instant count() queries: envSeries for the env's
// matcher, anySeries for the unfiltered one.
func fakeProm(t *testing.T, envSeries, anySeries string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		n := anySeries
		if strings.Contains(q, "service=~") {
			n = envSeries
		}
		if n == "" {
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1700000000,"` + n + `"]}]}}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("KUSO_PROMETHEUS_URL", srv.URL)
}

func TestClassifyTraffic(t *testing.T) {
	ctx := context.Background()
	m := envTraefikMatcher("kuso", "shop-web-production")

	if got := classifyTraffic(ctx, m, nil, errors.New("dial tcp: refused")); got != trafficUnavailable {
		t.Errorf("range error: %q", got)
	}
	if got := classifyTraffic(ctx, m, [][2]float64{{1, 0.5}}, nil); got != trafficOK {
		t.Errorf("with points: %q", got)
	}

	fakeProm(t, "3", "40")
	if got := classifyTraffic(ctx, m, nil, nil); got != trafficIdle {
		t.Errorf("env has counters: %q, want idle", got)
	}
	fakeProm(t, "", "40")
	if got := classifyTraffic(ctx, m, nil, nil); got != trafficNever {
		t.Errorf("ingress scraped, env never hit: %q, want never", got)
	}
	fakeProm(t, "", "")
	if got := classifyTraffic(ctx, m, nil, nil); got != trafficUnscraped {
		t.Errorf("no ingress metrics: %q, want unscraped", got)
	}

	t.Setenv("KUSO_PROMETHEUS_URL", "http://127.0.0.1:1")
	if got := classifyTraffic(ctx, m, nil, nil); got != trafficUnavailable {
		t.Errorf("prometheus down: %q, want unavailable", got)
	}
}
