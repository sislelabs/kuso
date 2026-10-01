package logs

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

type recordingSink struct {
	mu     sync.Mutex
	frames []Frame
}

func (r *recordingSink) Write(f Frame) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames = append(r.frames, f)
	return nil
}

func (r *recordingSink) logsFrom(pod string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, f := range r.frames {
		if f.Type == "log" && f.Pod == pod {
			n++
		}
	}
	return n
}

func testPod(name string) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso", UID: types.UID("uid-" + name)},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
		Status:     corev1.PodStatus{Phase: corev1.PodSucceeded},
	}
}

// Build and run streams send phase=completed only after streamPods
// returns. The heartbeat used to sit in the pod WaitGroup, so streamPods
// never returned while the client was connected.
func TestStreamPods_ReturnsWhenPodStreamsEnd(t *testing.T) {
	pod := testPod("b1")
	s := &Service{Kube: &kube.Client{Clientset: fake.NewSimpleClientset(&pod)}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sink := &recordingSink{}
	start := time.Now()
	if err := s.streamPods(ctx, "kuso", []corev1.Pod{pod}, 10, sink, nil); err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el > 5*time.Second {
		t.Fatalf("streamPods returned after %s; it should return once the pod's log ends", el)
	}
	if sink.logsFrom("b1") == 0 {
		t.Fatal("no log frames from the pod")
	}
}

// After a redeploy the old pods' streams end and new pods appear. An env
// tail must attach them instead of going silent.
func TestStreamPods_EnvTailAttachesNewPods(t *testing.T) {
	old := streamPodsRelistInterval
	streamPodsRelistInterval = 50 * time.Millisecond
	defer func() { streamPodsRelistInterval = old }()

	oldPod, newPod := testPod("web-old"), testPod("web-new")
	s := &Service{Kube: &kube.Client{Clientset: fake.NewSimpleClientset(&oldPod, &newPod)}}
	var calls atomic.Int32
	relist := func(context.Context) ([]corev1.Pod, error) {
		calls.Add(1)
		return []corev1.Pod{newPod}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	sink := &recordingSink{}
	errc := make(chan error, 1)
	go func() { errc <- s.streamPods(ctx, "kuso", []corev1.Pod{oldPod}, 10, sink, relist) }()

	deadline := time.Now().Add(3 * time.Second)
	for sink.logsFrom("web-new") == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if sink.logsFrom("web-new") == 0 {
		t.Fatalf("new pod never attached (relist calls: %d)", calls.Load())
	}
	if sink.logsFrom("web-old") == 0 {
		t.Fatal("original pod not streamed")
	}
}
