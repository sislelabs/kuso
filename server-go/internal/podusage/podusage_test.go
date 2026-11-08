package podusage

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

var k = Key{Project: "shop", Service: "web", Env: "shop-web-production"}

func TestStore_RingWrapsAndKeepsNewest(t *testing.T) {
	st := newStore(4, time.Hour)
	t0 := time.UnixMilli(1_000_000)
	for i := 0; i < 10; i++ {
		st.Record(t0.Add(time.Duration(i)*SampleInterval), map[Key]Sample{k: {CPUMilli: int32(i)}})
	}
	s := st.series[k]
	if len(s.buf) != 4 {
		t.Fatalf("buffer grew to %d, want capped at 4", len(s.buf))
	}
	got := s.ordered()
	for i, v := range got {
		if v.CPUMilli != int32(6+i) {
			t.Fatalf("ordered = %+v, want CPU 6..9 oldest first", got)
		}
	}
	h := st.History(k, t0.Add(9*SampleInterval), time.Hour, 0)
	if len(h.Samples) != 4 || h.Samples[0].CPUMilli != 6 || *h.Since != got[0].T {
		t.Fatalf("history = %+v", h)
	}
}

func TestStore_DropsSeriesNotSeenWithinRetention(t *testing.T) {
	st := newStore(10, time.Hour)
	other := Key{Project: "shop", Service: "api", Env: "shop-api-production"}
	t0 := time.Now()
	st.Record(t0, map[Key]Sample{k: {}, other: {}})
	st.Record(t0.Add(61*time.Minute), map[Key]Sample{k: {}})
	if _, ok := st.series[other]; ok {
		t.Fatal("series unseen for longer than retention was kept")
	}
	if _, ok := st.series[k]; !ok {
		t.Fatal("live series was dropped")
	}
}

func TestStore_HistoryRangeAndDownsample(t *testing.T) {
	st := NewStore()
	// Align to a 2m bucket so each bucket holds exactly 4 samples.
	t0 := time.UnixMilli(1_700_000_040_000).Truncate(2 * time.Minute)
	for i := 0; i < 8; i++ {
		st.Record(t0.Add(time.Duration(i)*SampleInterval), map[Key]Sample{k: {CPUMilli: int32(10 * (i + 1)), MemBytes: int64(100 * ((i + 2) % 4)), Pods: 1}})
	}
	now := t0.Add(7 * SampleInterval)

	raw := st.History(k, now, 2*time.Minute, 300)
	if raw.StepSeconds != 30 || len(raw.Samples) != 5 {
		t.Fatalf("2m range at full resolution: step=%d n=%d, want 30s and the 5 samples from now-2m", raw.StepSeconds, len(raw.Samples))
	}

	// 4m over 2 points forces 2m buckets.
	h := st.History(k, now, 4*time.Minute, 2)
	if h.StepSeconds != 120 || len(h.Samples) != 2 {
		t.Fatalf("step=%d samples=%+v", h.StepSeconds, h.Samples)
	}
	b0, b1 := h.Samples[0], h.Samples[1]
	if b0.T != t0.UnixMilli() || b0.CPUMilli != 25 || b0.MemBytes != 300 || b0.Pods != 1 {
		t.Errorf("bucket 0 = %+v, want avg cpu 25, max mem 300", b0)
	}
	if b1.CPUMilli != 65 || b1.MemBytes != 300 {
		t.Errorf("bucket 1 = %+v, want avg cpu 65, max mem 300", b1)
	}

	if e := st.History(Key{Project: "nope"}, now, time.Hour, 300); len(e.Samples) != 0 || e.Since != nil || e.Samples == nil {
		t.Errorf("unknown series = %+v, want empty non-nil samples and nil since", e)
	}
}

func TestStore_DownsampleCapsPoints(t *testing.T) {
	st := NewStore()
	t0 := time.Now().Add(-Retention)
	for i := 0; i < Capacity; i++ {
		st.Record(t0.Add(time.Duration(i)*SampleInterval), map[Key]Sample{k: {CPUMilli: 1}})
	}
	h := st.History(k, t0.Add(time.Duration(Capacity-1)*SampleInterval), 24*time.Hour, 300)
	if len(h.Samples) > 301 || len(h.Samples) < 250 {
		t.Fatalf("24h downsampled to %d points, want ~300", len(h.Samples))
	}
	if h.StepSeconds != 300 {
		t.Errorf("step = %ds, want 300 (24h / 300 points rounded up to 30s)", h.StepSeconds)
	}
}

func pod(ns, name, node string, phase corev1.PodPhase, lbl map[string]string) *corev1.Pod {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: lbl}}
	p.Spec.NodeName = node
	p.Status.Phase = phase
	return p
}

func envLabels(project, service, env string) map[string]string {
	return map[string]string{kube.LabelProject: project, kube.LabelService: service, labelInstance: env, labelName: envChartName}
}

func TestAggregate_FromKubeletSummary(t *testing.T) {
	// Real kubelet payloads, parsed the way the sampler does.
	n1, err := kube.ParseSummaryUsage([]byte(`{"node":{"fs":{"capacityBytes":10}},"pods":[
		{"podRef":{"name":"web-a","namespace":"kuso"},"cpu":{"usageNanoCores":120400000},"memory":{"workingSetBytes":100}},
		{"podRef":{"name":"api-a","namespace":"kuso"},"cpu":{"usageNanoCores":5000000},"memory":{"workingSetBytes":7}},
		{"podRef":{"name":"web-new","namespace":"kuso"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	n2, err := kube.ParseSummaryUsage([]byte(`{"node":{"fs":{"capacityBytes":10}},"pods":[
		{"podRef":{"name":"web-b","namespace":"kuso"},"cpu":{"usageNanoCores":30000000},"memory":{"workingSetBytes":50}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	usage := map[string][]kube.PodUsage{"n1": n1.Pods, "n2": n2.Pods}
	web := envLabels("shop", "shop-web", "shop-web-production")
	pods := []*corev1.Pod{
		pod("kuso", "web-a", "n1", corev1.PodRunning, web),
		pod("kuso", "web-b", "n2", corev1.PodRunning, web),
		pod("kuso", "web-new", "n1", corev1.PodRunning, web),    // no stats yet
		pod("kuso", "web-done", "n1", corev1.PodSucceeded, web), // not running
		pod("kuso", "api-a", "n1", corev1.PodRunning, envLabels("shop", "api", "shop-api-production")),
		pod("kuso", "api-b", "n3", corev1.PodRunning, envLabels("shop", "api", "shop-api-production")), // n3 failed
		pod("kuso", "stray", "n1", corev1.PodRunning, map[string]string{kube.LabelProject: "shop"}),
	}
	got := Aggregate(pods, usage)
	want := map[Key]Sample{
		{Project: "shop", Service: "web", Env: "shop-web-production"}: {CPUMilli: 150, MemBytes: 150, Pods: 2},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v (api must be dropped: one replica's node failed)", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%+v = %+v, want %+v", k, got[k], v)
		}
	}
}
