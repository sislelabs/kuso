package cronwatch

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

func failedJob(name, cron, uid string) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "kuso",
			UID:       types.UID(uid),
			Labels:    map[string]string{"kuso.sislelabs.com/cron": cron},
		},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobFailed, Status: corev1.ConditionTrue},
			},
		},
	}
}

func seedCronCR(t *testing.T, dyn *dynamicfake.FakeDynamicClient, name, webhookURL string) {
	t.Helper()
	cron := &kube.KusoCron{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso"},
		Spec: kube.KusoCronSpec{
			Project:   "alpha",
			Service:   "web",
			OnFailure: &kube.KusoCronOnFailure{WebhookURL: webhookURL},
		},
	}
	u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cron)
	if err != nil {
		t.Fatalf("to unstructured: %v", err)
	}
	obj := &unstructured.Unstructured{Object: u}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group: kube.GVRCrons.Group, Version: kube.GVRCrons.Version, Kind: "KusoCron",
	})
	if err := dyn.Tracker().Create(kube.GVRCrons, obj, "kuso"); err != nil {
		t.Fatalf("seed cron CR: %v", err)
	}
}

// TestTick_DispatchesConcurrently is the SEC-5b regression: two failed
// crons whose webhooks each take ~200ms must dispatch in parallel, so
// the whole tick finishes in ~200ms rather than ~400ms serial. Also
// exercises the concurrent handler goroutines under -race.
func TestTick_DispatchesConcurrently(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cs := fake.NewSimpleClientset(
		failedJob("job-a", "cron-a", "uid-a"),
		failedJob("job-b", "cron-b", "uid-b"),
	)
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		kube.GVRCrons: "KusoCronList",
	})
	seedCronCR(t, dyn, "cron-a", srv.URL)
	seedCronCR(t, dyn, "cron-b", srv.URL)

	w := &Watcher{
		Kube:   &kube.Client{Clientset: cs, Dynamic: dyn},
		Config: Config{}, Logger: slog.Default(),
		dispatched: map[types.UID]struct{}{},
	}

	start := time.Now()
	w.tick(context.Background())
	elapsed := time.Since(start)

	if got := atomic.LoadInt64(&hits); got != 2 {
		t.Fatalf("webhook hits = %d, want 2", got)
	}
	// Serial would be ~400ms; concurrent ~200ms. Allow slack.
	if elapsed > 350*time.Millisecond {
		t.Errorf("tick took %v — handlers appear to run serially, not concurrently", elapsed)
	}
}

// TestTick_DedupesAcrossTicks: a failed Job already dispatched must not
// re-fire on the next tick.
func TestTick_DedupesAcrossTicks(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cs := fake.NewSimpleClientset(failedJob("job-a", "cron-a", "uid-a"))
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		kube.GVRCrons: "KusoCronList",
	})
	seedCronCR(t, dyn, "cron-a", srv.URL)

	w := &Watcher{
		Kube:   &kube.Client{Clientset: cs, Dynamic: dyn},
		Config: Config{}, Logger: slog.Default(),
		dispatched: map[types.UID]struct{}{},
	}
	w.tick(context.Background())
	w.tick(context.Background())
	if got := atomic.LoadInt64(&hits); got != 1 {
		t.Errorf("webhook hits = %d, want 1 (deduped across ticks)", got)
	}
}
