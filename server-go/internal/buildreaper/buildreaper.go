// Package buildreaper closes the operator-resurrection hole on
// KusoBuild CRs.
//
// Today: kuso-server's Cancel path stamps the CR, deletes the Job,
// and deletes the helm-release Secret. The helm-release secret
// deletion is the load-bearing step — without it, the helm-operator's
// next reconcile (or its initial cache sync after a restart) finds
// "release exists, manifest says Job should exist, Job is missing"
// and re-renders the kaniko Job. That class of bug burned us on
// 2026-05-05 when a cancelled build kept respawning every 30s until
// the operator was scaled to 0.
//
// This package is the minimum-viable companion controller: a
// goroutine that watches KusoBuild CRs via the existing dynamic
// informer, and whenever it observes a build transition to
// `done=true` (or with label `build-state=done`), it deletes any
// helm-release Secret labelled `owner=helm,name=<build>` in the
// same namespace. The reaper is **idempotent** — if the secret was
// already deleted by the Cancel path, the NotFound is swallowed.
//
// Why a small reaper rather than a full Go controller replacing the
// helm chart: the chart is 730 lines of init containers, kaniko
// config, buildkitd wiring, cache-PVC mounting, and per-runtime
// conditionals. Porting that to Go is a multi-week project. The
// reaper closes the specific operator-resurrection bug class with
// ~150 lines and no new deps, and the chart stays the source of
// truth for Job rendering.
//
// Future Go controller: the natural next step is a controller-
// runtime Reconciler that owns the Job lifecycle directly (build
// CR → Job + Pod, no helm in between). That would also evaporate
// the spec.done=true / spec.image.tag="" chart-defang dance. Until
// then, the reaper + the chart's no-op gate + the watch selector
// are the three belt-and-braces patches keeping the seam together.
package buildreaper

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/cache"

	"kuso/server/internal/kube"
)

// Service holds the dependencies the reaper needs. The Cache is
// where we install the event handler; the kube client is what we
// use to delete the helm-release Secret on done-transition.
// LIFETIME: Start must be called exactly once at boot, NOT once
// per leader-election cycle. The previous shape registered a new
// handler on every leader acquire, so after N re-elections every
// done-transition fired N reap goroutines that all hit the same
// helm-release Secret. sync.Once keeps Start idempotent against
// programming errors. LeaderActive gates the work, not the
// registration.
type Service struct {
	Kube         *kube.Client
	Cache        *kube.Cache
	Logger       *slog.Logger
	// LeaderActive gates per-event reaping. nil = always-active.
	// Reaping a helm secret while another replica is doing the same
	// is idempotent (NotFound is swallowed), but only the lease
	// holder should be issuing the calls in normal operation.
	LeaderActive *atomic.Bool

	// reaped tracks Build CRs we've already reaped this process
	// lifetime. The informer can fire multiple update events for the
	// same final state (status patches from the helm-operator continue
	// after our cancel stamp) and reaping twice is wasted API calls.
	mu     sync.Mutex
	reaped map[string]struct{}

	startOnce sync.Once
}

// Start installs the AddEventHandler on the KusoBuild informer.
// Returns immediately; the handler runs from the informer worker.
// Idempotent — only the first call wires the handler. Call once at
// boot.
func (s *Service) Start(ctx context.Context) {
	if s == nil || s.Cache == nil || s.Kube == nil {
		return
	}
	s.startOnce.Do(func() { s.installHandler(ctx) })
}

func (s *Service) installHandler(ctx context.Context) {
	if s.reaped == nil {
		s.reaped = make(map[string]struct{})
	}
	inf := s.Cache.CRDInformer(kube.GVRBuilds)
	if inf == nil {
		if s.Logger != nil {
			s.Logger.Warn("buildreaper: no informer for KusoBuild — skipped")
		}
		return
	}
	_, err := inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			s.maybeReap(ctx, obj, "add")
		},
		UpdateFunc: func(_, newObj any) {
			s.maybeReap(ctx, newObj, "update")
		},
	})
	if err != nil && s.Logger != nil {
		s.Logger.Warn("buildreaper: AddEventHandler", "err", err)
	}
	if s.Logger != nil {
		s.Logger.Info("buildreaper: started — watching KusoBuild transitions to done=true")
	}
}

// maybeReap inspects the observed CR and reaps helm-release secrets
// when the build is done. Non-done CRs are ignored. The reaped map
// dedups against repeated informer notifications (status patches keep
// flowing from the operator even after we mark the CR done).
func (s *Service) maybeReap(ctx context.Context, obj any, source string) {
	if s.LeaderActive != nil && !s.LeaderActive.Load() {
		return
	}
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return
	}
	if !isDone(u) {
		return
	}
	key := u.GetNamespace() + "/" + u.GetName()
	s.mu.Lock()
	if _, already := s.reaped[key]; already {
		s.mu.Unlock()
		return
	}
	s.reaped[key] = struct{}{}
	s.mu.Unlock()

	// 5s budget — the Secrets.List + Delete should complete in well
	// under a second on a healthy apiserver. The reaper is best-
	// effort: if the call times out the next Cancel-or-restart cycle
	// gets another shot.
	reapCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	s.reapHelmSecrets(reapCtx, u.GetNamespace(), u.GetName(), source)
}

// reapHelmSecrets deletes every helm-release Secret labelled with
// owner=helm,name=<build>. The helm-operator stores release state
// in Secrets named `sh.helm.release.v1.<build>.v1` with these labels;
// dropping them turns the release into a fresh-install from the
// operator's perspective, so when the next reconcile runs (race we
// can't easily avoid) it re-installs a release with our defanged
// values (spec.done=true → chart renders zero objects).
func (s *Service) reapHelmSecrets(ctx context.Context, ns, build, source string) {
	selector := "owner=helm,name=" + build
	secs, err := s.Kube.Clientset.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		if s.Logger != nil && !errors.Is(err, context.DeadlineExceeded) {
			s.Logger.Warn("buildreaper: list helm release secrets",
				"err", err, "build", build, "ns", ns, "source", source)
		}
		return
	}
	if len(secs.Items) == 0 {
		return
	}
	deleted := 0
	for i := range secs.Items {
		name := secs.Items[i].Name
		if derr := s.Kube.Clientset.CoreV1().Secrets(ns).Delete(ctx, name, metav1.DeleteOptions{}); derr != nil {
			if !apierrors.IsNotFound(derr) {
				if s.Logger != nil {
					s.Logger.Warn("buildreaper: delete helm release secret",
						"err", derr, "secret", name, "build", build, "ns", ns)
				}
			}
			continue
		}
		deleted++
	}
	if deleted > 0 && s.Logger != nil {
		s.Logger.Info("buildreaper: reaped helm-release secrets",
			"build", build, "ns", ns, "deleted", deleted, "source", source)
	}
}

// isDone returns true for CRs that have completed (succeeded /
// failed / cancelled). Two signal sources: spec.done=true (set by
// kuso-server's Cancel + markSucceeded / markFailed paths so the
// chart guard short-circuits) AND label kuso.sislelabs.com/build-
// state=done (set on the same writes; the operator's watch
// selector excludes this label). Either signal triggers reaping;
// the helm-secret delete is idempotent against re-firing.
func isDone(u *unstructured.Unstructured) bool {
	if u == nil {
		return false
	}
	if v, ok, _ := unstructured.NestedBool(u.Object, "spec", "done"); ok && v {
		return true
	}
	if labels := u.GetLabels(); labels != nil {
		if labels["kuso.sislelabs.com/build-state"] == "done" {
			return true
		}
	}
	return false
}
