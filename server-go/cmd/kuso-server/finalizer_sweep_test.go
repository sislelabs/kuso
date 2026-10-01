package main

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuso/server/internal/kube"
)

// Projects with a custom spec.namespace must be swept too, not only the
// home namespace.
func TestFinalizerSweepCoversEveryNamespace(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			kube.GVREnvironments: "KusoEnvironmentList",
			kube.GVRServices:     "KusoServiceList",
			kube.GVRAddons:       "KusoAddonList",
			kube.GVRProjects:     "KusoProjectList",
			kube.GVRCrons:        "KusoCronList",
			kube.GVRRuns:         "KusoRunList",
		})
	var mu sync.Mutex
	listed := map[string]bool{}
	dyn.PrependReactor("list", "kusoenvironments", func(a k8stesting.Action) (bool, runtime.Object, error) {
		mu.Lock()
		listed[a.GetNamespace()] = true
		mu.Unlock()
		return false, nil, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runFinalizerSweep(ctx, &kube.Client{Dynamic: dyn},
			func(context.Context) []string { return []string{"kuso", "koreni"} },
			slog.New(slog.NewTextHandler(io.Discard, nil)))
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		ok := listed["kuso"] && listed["koreni"]
		mu.Unlock()
		if ok {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("namespaces swept = %v, want kuso and koreni", listed)
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
}
