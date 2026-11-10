// Package leader wraps client-go's leaderelection so the singleton
// background workers (build poller, alert engine, nodewatch, the
// updater poller, daily cleanup, error scan) only run on one
// kuso-server pod even when the Deployment is scaled to multiple
// replicas.
//
// Without this, scaling replicas above 1 silently double-promotes
// builds, double-emits notification events, and double-archives
// build logs because every replica's poller runs the same tick on
// the same CRs.
//
// Usage:
//
//	go leader.RunWhenLeader(ctx, leader.Config{
//	    Namespace: "kuso",
//	    LockName:  "kuso-singletons",
//	    Identity:  podName(),
//	    Client:    kc.Clientset,
//	    Logger:    logger,
//	    Run: func(leaderCtx context.Context) {
//	        // Start every singleton goroutine using leaderCtx.
//	    },
//	})
//
// The provided Run callback is invoked once we acquire the lease; it
// is expected to spawn its goroutines and return. When the lease is
// lost (apiserver hiccup, pod eviction, demotion), leaderCtx is
// canceled and the callback's goroutines should unwind. If the lease
// is reacquired later in the same process, Run is invoked again with
// a fresh ctx — callbacks must therefore be safe to invoke multiple
// times.
package leader

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

// metaFor builds the ObjectMeta for the Lease lock object. Lease
// objects live in the same namespace as the kuso-server pod so the
// existing RBAC grant covers them without needing cluster-wide
// permission.
func metaFor(namespace, name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:      name,
		Namespace: namespace,
		Labels: map[string]string{
			"app.kubernetes.io/name":       "kuso-server",
			"app.kubernetes.io/component":  "leader-election",
			"app.kubernetes.io/managed-by": "kuso-server",
		},
	}
}

// Config bundles the inputs RunWhenLeader needs. Identity defaults to
// the host's name when empty (set KUSO_POD_NAME or rely on os.Hostname).
type Config struct {
	Namespace string
	LockName  string
	Identity  string
	Client    kubernetes.Interface
	Logger    *slog.Logger
	// Run is called when this process acquires the lease. The provided
	// context is canceled when the lease is lost; callers should pass
	// it to every goroutine they spawn.
	Run func(leaderCtx context.Context)

	// LeaseDuration / RenewDeadline / RetryPeriod control how
	// aggressively we hold the lease. Defaults match the kube
	// recommended ratios (15s / 10s / 2s) — eager enough to fail
	// over fast on a pod kill, lax enough that a slow apiserver
	// doesn't churn leadership.
	LeaseDuration time.Duration
	RenewDeadline time.Duration
	RetryPeriod   time.Duration

	// PreferNotHolding names another lock this one should preferably
	// land away from. While this process holds that lock it waits
	// DeferFor before contesting, so with 2+ replicas another pod picks
	// this lease up first and the two singleton groups spread across
	// pods. A single replica still acquires it after the wait. The lock
	// name and the loops it guards are unchanged, so old and new pods
	// in a mixed rollout still contend for the same lease.
	PreferNotHolding string
	DeferFor         time.Duration
	// HasPeers, when set, skips the deferral if no other replica is
	// running, so a single-replica install doesn't start its second
	// singleton group late on every boot.
	HasPeers func() bool
}

var (
	heldMu sync.Mutex
	held   = map[string]bool{}
)

// Holds reports whether this process currently leads lockName.
func Holds(lockName string) bool {
	heldMu.Lock()
	defer heldMu.Unlock()
	return held[lockName]
}

func setHeld(lockName string, v bool) {
	heldMu.Lock()
	defer heldMu.Unlock()
	if v {
		held[lockName] = true
	} else {
		delete(held, lockName)
	}
}

// settleBeforeContest is how long a lease with PreferNotHolding waits on
// its first contest so the preferred-away lease has had a chance to be
// decided (both electors start at the same moment on boot).
var settleBeforeContest = 3 * time.Second

// waitForPreference delays contesting while this process holds the lock
// it prefers to avoid. Returns false when ctx ends.
func waitForPreference(ctx context.Context, cfg Config, first bool) bool {
	if cfg.PreferNotHolding == "" {
		return true
	}
	if first {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(settleBeforeContest):
		}
	}
	if !Holds(cfg.PreferNotHolding) {
		return true
	}
	if cfg.HasPeers != nil && !cfg.HasPeers() {
		return true
	}
	d := cfg.DeferFor
	if d <= 0 {
		d = 2 * cfg.LeaseDuration
	}
	if cfg.Logger != nil {
		cfg.Logger.Info("leader: deferring contest so another replica can take this lease",
			"lock", cfg.Namespace+"/"+cfg.LockName, "holding", cfg.PreferNotHolding, "for", d)
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// RunWhenLeader blocks until ctx is canceled, repeatedly contesting
// the lease and invoking cfg.Run when this process is the leader.
// Returns when ctx is canceled — the goroutine that called this
// function should be the one that holds the parent context.
func RunWhenLeader(ctx context.Context, cfg Config) {
	if cfg.Run == nil {
		return
	}
	if cfg.Identity == "" {
		host, err := os.Hostname()
		if err == nil {
			cfg.Identity = host
		}
	}
	if cfg.LeaseDuration == 0 {
		cfg.LeaseDuration = 15 * time.Second
	}
	if cfg.RenewDeadline == 0 {
		cfg.RenewDeadline = 10 * time.Second
	}
	if cfg.RetryPeriod == 0 {
		cfg.RetryPeriod = 2 * time.Second
	}

	lock := &resourcelock.LeaseLock{
		LeaseMeta: metaFor(cfg.Namespace, cfg.LockName),
		Client:    cfg.Client.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{
			Identity: cfg.Identity,
		},
	}

	first := true
	for {
		if ctx.Err() != nil {
			return
		}
		if !waitForPreference(ctx, cfg, first) {
			return
		}
		first = false
		// Each elector instance runs until the lease is lost; we then
		// loop and contest again. RunOrDie panics on certain
		// configuration errors, so use Run + observe the returned
		// error explicitly.
		le, err := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{
			Lock:            lock,
			ReleaseOnCancel: true,
			LeaseDuration:   cfg.LeaseDuration,
			RenewDeadline:   cfg.RenewDeadline,
			RetryPeriod:     cfg.RetryPeriod,
			Callbacks: leaderelection.LeaderCallbacks{
				OnStartedLeading: func(leaderCtx context.Context) {
					setHeld(cfg.LockName, true)
					if cfg.Logger != nil {
						cfg.Logger.Info("leader: acquired lease",
							"lock", cfg.Namespace+"/"+cfg.LockName,
							"identity", cfg.Identity)
					}
					cfg.Run(leaderCtx)
				},
				OnStoppedLeading: func() {
					setHeld(cfg.LockName, false)
					if cfg.Logger != nil {
						cfg.Logger.Warn("leader: lost lease",
							"lock", cfg.Namespace+"/"+cfg.LockName,
							"identity", cfg.Identity)
					}
				},
				OnNewLeader: func(id string) {
					if cfg.Logger != nil && id != cfg.Identity {
						cfg.Logger.Info("leader: someone else won",
							"lock", cfg.Namespace+"/"+cfg.LockName,
							"new_leader", id, "self", cfg.Identity)
					}
				},
			},
		})
		if err != nil {
			if cfg.Logger != nil {
				cfg.Logger.Error("leader: build elector", "err", err)
			}
			// Back off and try again. Returning would mean the
			// singletons never run, which is worse than a noisy log.
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
				continue
			}
		}
		le.Run(ctx)
	}
}
