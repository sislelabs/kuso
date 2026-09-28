package scaledown

import (
	"context"
	"log/slog"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// TestHasActiveJobs covers MED-1e: scaledown must not sleep a service
// while one of its cron/run Jobs is mid-flight. Jobs are matched by the
// kuso.sislelabs.com/service=<fqn> label; an Active count > 0 means
// in-flight. A LIST failure isn't exercised here (fake never errors), but
// the code fails safe by reporting "active".
func TestHasActiveJobs(t *testing.T) {
	t.Parallel()

	const ns, fqn = "kuso-alpha", "alpha-web"
	job := func(name, svc string, active int32) *batchv1.Job {
		return &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
				Labels:    map[string]string{"kuso.sislelabs.com/service": svc},
			},
			Status: batchv1.JobStatus{Active: active},
		}
	}

	cases := []struct {
		name string
		jobs []*batchv1.Job
		want bool
	}{
		{"no jobs", nil, false},
		{"labelled job active", []*batchv1.Job{job("j1", fqn, 1)}, true},
		{"labelled job finished (active 0)", []*batchv1.Job{job("j1", fqn, 0)}, false},
		{"active job for a different service", []*batchv1.Job{job("j1", "alpha-api", 1)}, false},
		{"mixed: one other done, one ours active", []*batchv1.Job{job("j1", fqn, 0), job("j2", fqn, 2)}, true},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			objs := make([]runtime.Object, 0, len(c.jobs))
			for _, j := range c.jobs {
				objs = append(objs, j)
			}
			cs := fake.NewSimpleClientset(objs...)
			w := &Watcher{Kube: &kube.Client{Clientset: cs}, Logger: slog.Default()}
			if got := w.hasActiveJobs(context.Background(), ns, fqn); got != c.want {
				t.Errorf("%s: hasActiveJobs = %v, want %v", c.name, got, c.want)
			}
		})
	}
}

// TestActivatorReady covers the H4 fail-safe: scale-to-zero must never
// fire while kuso-activator (the only thing that can wake a slept
// service) has no ready replica. The activator is a separate Deployment
// that ship/updater don't roll, so "activator down" is a real,
// previously-observed state — scaling to 0 then is hard downtime.
func TestActivatorReady(t *testing.T) {
	t.Parallel()

	dep := func(ready int32) *appsv1.Deployment {
		return &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: activatorDeployment, Namespace: "kuso"},
			Status:     appsv1.DeploymentStatus{ReadyReplicas: ready},
		}
	}

	cases := []struct {
		name string
		objs []runtime.Object
		want bool
	}{
		{"activator ready (1 replica) → scale-down allowed", []runtime.Object{dep(1)}, true},
		{"activator ready (2 replicas) → scale-down allowed", []runtime.Object{dep(2)}, true},
		{"activator deployed but 0 ready → skip tick", []runtime.Object{dep(0)}, false},
		{"activator deployment absent → skip tick", nil, false},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			cs := fake.NewSimpleClientset(c.objs...)
			w := &Watcher{
				Kube:      &kube.Client{Clientset: cs},
				Namespace: "kuso",
				Logger:    slog.Default(),
			}
			if got := w.activatorReady(context.Background()); got != c.want {
				t.Errorf("activatorReady = %v, want %v", got, c.want)
			}
		})
	}

	// Empty Namespace falls back to the "kuso" control-plane namespace.
	t.Run("default namespace fallback", func(t *testing.T) {
		t.Parallel()
		cs := fake.NewSimpleClientset(dep(1))
		w := &Watcher{Kube: &kube.Client{Clientset: cs}, Logger: slog.Default()}
		if !w.activatorReady(context.Background()) {
			t.Error("empty Namespace should resolve to kuso and find the ready activator")
		}
	})
}

func TestEscapePromLabel(t *testing.T) {
	t.Parallel()
	// dots and dashes appear in real ns-env names; dots must be escaped
	// (regex any-char) but dashes are literal in PromQL =~.
	got := escapePromLabel("kuso-papelito-web-production")
	want := "kuso-papelito-web-production"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := escapePromLabel("a.b"); got != `a\.b` {
		t.Errorf("dot escape: got %q", got)
	}
}
