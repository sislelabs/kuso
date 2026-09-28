package drains

import (
	"bytes"
	"testing"
)

func TestMatchTraefikServicePicksLongestEnvPrefix(t *testing.T) {
	envs := []EnvRef{
		{Namespace: "kuso", Name: "shop-web", Project: "shop", Service: "web"},
		{Namespace: "kuso", Name: "shop-web-pr-3", Project: "shop", Service: "web"},
		{Namespace: "tenant", Name: "blog-app", Project: "blog", Service: "app"},
	}
	cases := map[string]string{
		"kuso-shop-web-8080@kubernetes":      "shop-web",
		"kuso-shop-web-pr-3-8080@kubernetes": "shop-web-pr-3",
		"kuso-shop-web@kubernetes":           "shop-web",
		"tenant-blog-app-http@kubernetes":    "blog-app",
		"kuso-blog-app-http@kubernetes":      "",
		"kuso-shop-webby-8080@kubernetes":    "",
		"dashboard@internal":                 "",
	}
	for label, want := range cases {
		got, ok := MatchTraefikService(label, envs)
		if want == "" {
			if ok {
				t.Errorf("%s matched %s, want no match", label, got.Name)
			}
			continue
		}
		if !ok || got.Name != want {
			t.Errorf("%s → %q (ok=%v), want %q", label, got.Name, ok, want)
		}
	}
}

func TestWritePrometheusGolden(t *testing.T) {
	samples := []EnvSample{
		{Env: EnvRef{Project: "shop", Service: "web", Name: "shop-web"}, HasTraffic: true, RequestsPerSec: 12.5, Errors5xxPerSec: 0.25, HasP95: true, P95Seconds: 0.182,
			HasResources: true, CPUCores: 0.25, MemoryBytes: 268435456, Pods: 2},
		{Env: EnvRef{Project: "a\"b", Service: "w", Name: "x"}, HasResources: true, CPUCores: 0.001, MemoryBytes: 1024, Pods: 1},
	}
	var buf bytes.Buffer
	if err := WritePrometheus(&buf, samples); err != nil {
		t.Fatal(err)
	}
	want := `# HELP kuso_env_http_requests_per_second Request rate over the last 5m, from Traefik.
# TYPE kuso_env_http_requests_per_second gauge
kuso_env_http_requests_per_second{project="shop",service="web",env="shop-web"} 12.5
# HELP kuso_env_http_5xx_per_second 5xx response rate over the last 5m, from Traefik.
# TYPE kuso_env_http_5xx_per_second gauge
kuso_env_http_5xx_per_second{project="shop",service="web",env="shop-web"} 0.25
# HELP kuso_env_http_p95_latency_seconds p95 request latency over the last 5m, from Traefik.
# TYPE kuso_env_http_p95_latency_seconds gauge
kuso_env_http_p95_latency_seconds{project="shop",service="web",env="shop-web"} 0.182
# HELP kuso_env_cpu_cores CPU in use across the env's pods, from metrics-server.
# TYPE kuso_env_cpu_cores gauge
kuso_env_cpu_cores{project="a\"b",service="w",env="x"} 0.001
kuso_env_cpu_cores{project="shop",service="web",env="shop-web"} 0.25
# HELP kuso_env_memory_bytes Memory in use across the env's pods, from metrics-server.
# TYPE kuso_env_memory_bytes gauge
kuso_env_memory_bytes{project="a\"b",service="w",env="x"} 1024
kuso_env_memory_bytes{project="shop",service="web",env="shop-web"} 2.68435456e+08
# HELP kuso_env_pods Pods reporting metrics for the env.
# TYPE kuso_env_pods gauge
kuso_env_pods{project="a\"b",service="w",env="x"} 1
kuso_env_pods{project="shop",service="web",env="shop-web"} 2
`
	if buf.String() != want {
		t.Fatalf("exposition mismatch\n--- got ---\n%s\n--- want ---\n%s", buf.String(), want)
	}
}

func TestAssembleEnvSamples(t *testing.T) {
	envs := []EnvRef{
		{Namespace: "kuso", Name: "shop-web", Project: "shop", Service: "web"},
		{Namespace: "kuso", Name: "shop-worker", Project: "shop", Service: "worker"},
		{Namespace: "kuso", Name: "idle", Project: "p", Service: "idle"},
	}
	traffic := Traffic{
		Requests: map[string]float64{"kuso-shop-web-8080@kubernetes": 10, "kuso-shop-web-9090@kubernetes": 2, "kuso-ghost-1@kubernetes": 99},
		Errors:   map[string]float64{"kuso-shop-web-8080@kubernetes": 0.5},
		P95:      map[string]float64{"kuso-shop-web-8080@kubernetes": 0.1, "kuso-shop-web-9090@kubernetes": 0.3},
	}
	res := map[string]Resources{"shop-worker": {CPUMilli: 250, MemBytes: 1 << 20, Pods: 1}}
	got := AssembleEnvSamples(envs, traffic, res)
	if len(got) != 2 {
		t.Fatalf("want 2 envs with data (idle omitted), got %+v", got)
	}
	by := map[string]EnvSample{}
	for _, s := range got {
		by[s.Env.Name] = s
	}
	web := by["shop-web"]
	// Rates sum across the env's ports; p95 takes the worst port.
	if !web.HasTraffic || web.RequestsPerSec != 12 || web.Errors5xxPerSec != 0.5 || !web.HasP95 || web.P95Seconds != 0.3 || web.HasResources {
		t.Fatalf("web sample wrong: %+v", web)
	}
	w := by["shop-worker"]
	if w.HasTraffic || !w.HasResources || w.CPUCores != 0.25 || w.MemoryBytes != 1<<20 || w.Pods != 1 {
		t.Fatalf("worker sample wrong: %+v", w)
	}
}
