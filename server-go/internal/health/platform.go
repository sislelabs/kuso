package health

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/notify"
)

// Platform workloads carry no kuso project label, so checkPods never
// sees them: a crashlooping Traefik, an orphaned buildkitd or an OOMing
// registry used to page nobody. checkPlatform watches a fixed list.

// PlatformTarget is one platform workload. Either Deployment (its own
// selector is used) or Selector (for pods with no Deployment, e.g. the
// CNPG control-plane Postgres). Namespace "" means the kuso home ns.
type PlatformTarget struct {
	Component  string
	Namespace  string
	Deployment string
	Selector   string
}

// platformTargets mirrors the workloads platformharden hardens plus the
// kuso-owned ones in the home namespace. Missing workloads are skipped,
// so optional components (activator, pgbouncer, a Traefik that lives in
// kube-system instead of traefik) cost nothing.
var platformTargets = []PlatformTarget{
	{Component: "Traefik", Namespace: "traefik", Deployment: "traefik"},
	{Component: "Traefik", Namespace: "kube-system", Deployment: "traefik"},
	{Component: "kuso-server", Deployment: "kuso-server"},
	{Component: "buildkitd", Deployment: "kuso-buildkitd"},
	{Component: "registry", Deployment: "kuso-registry"},
	{Component: "activator", Deployment: "kuso-activator"},
	{Component: "Prometheus", Deployment: "kuso-prometheus"},
	{Component: "pgbouncer", Deployment: "kuso-pgbouncer"},
	{Component: "control-plane Postgres", Selector: "cnpg.io/cluster=kuso-postgres"},
	{Component: "kuso-operator", Namespace: "kuso-operator-system", Deployment: "kuso-operator-controller-manager"},
	{Component: "cert-manager", Namespace: "cert-manager", Deployment: "cert-manager"},
	{Component: "cert-manager-webhook", Namespace: "cert-manager", Deployment: "cert-manager-webhook"},
}

const (
	// PlatformCrashGrace: a crashloop / image-pull failure must persist
	// this long before paging. The first tick only opens the episode, so
	// a server roll mid-blip (or on upgrade) never pages on sight.
	PlatformCrashGrace = 2 * time.Minute
	// PlatformUnreadyGrace: no ready pod for this long pages. Longer than
	// a normal rollout or node drain reschedule.
	PlatformUnreadyGrace = 5 * time.Minute
	// platformOOMWindow / platformOOMCount: repeated OOM kills page even
	// when the pod comes back Ready between ticks (the 2026-10-08
	// Traefik OOM pattern).
	platformOOMWindow = 15 * time.Minute
	platformOOMCount  = 3
)

// platformEpisode is one target's open bad stretch.
type platformEpisode struct {
	badSince     time.Time
	alerted      bool
	healthySince time.Time
}

// platformKey identifies a target.
func (t PlatformTarget) key(home string) string {
	ns := t.Namespace
	if ns == "" {
		ns = home
	}
	return ns + "/" + t.Deployment + "/" + t.Selector
}

// platformObservation is one tick's verdict for a target.
type platformObservation struct {
	absent  bool // no such workload, or scaled to zero on purpose
	crash   string
	unready bool
	ready   bool
}

func (w *Watcher) checkPlatform(ctx context.Context) {
	now := w.clock()
	home := w.Namespace
	if home == "" {
		home = "kuso"
	}
	w.mu.Lock()
	if w.platformSeen == nil {
		w.platformSeen = map[string]*platformEpisode{}
	}
	if w.platformOOMs == nil {
		w.platformOOMs = map[string][]time.Time{}
	}
	w.mu.Unlock()
	var events []notify.Event
	for _, t := range platformTargets {
		ns := t.Namespace
		if ns == "" {
			ns = home
		}
		key := t.key(home)
		obs, ok := w.observePlatform(ctx, t, ns, key, now)
		if !ok {
			continue // unknown this tick: leave state alone
		}
		w.mu.Lock()
		ep := w.platformSeen[key]
		bad := obs.crash != "" || obs.unready
		switch {
		case obs.absent:
			delete(w.platformSeen, key)
		case bad:
			if ep == nil {
				ep = &platformEpisode{badSince: now}
				w.platformSeen[key] = ep
			}
			ep.healthySince = time.Time{}
			grace := PlatformUnreadyGrace
			reason := "no ready pod"
			if obs.crash != "" {
				grace, reason = PlatformCrashGrace, obs.crash
			}
			if !ep.alerted && now.Sub(ep.badSince) >= grace {
				ep.alerted = true
				events = append(events, notify.PlatformUnhealthy(t.Component, ns, reason, ep.badSince))
			}
		case ep != nil && ep.alerted:
			if !obs.ready {
				ep.healthySince = time.Time{}
				break
			}
			if ep.healthySince.IsZero() {
				ep.healthySince = now
			}
			if now.Sub(ep.healthySince) >= RecoveryStableWindow {
				events = append(events, notify.PlatformRecovered(t.Component, ep.healthySince.Sub(ep.badSince)))
				delete(w.platformSeen, key)
			}
		default:
			delete(w.platformSeen, key)
		}
		w.mu.Unlock()
	}
	for _, e := range events {
		w.send(e)
	}
}

// observePlatform reads one target. ok=false means it couldn't be read.
func (w *Watcher) observePlatform(ctx context.Context, t PlatformTarget, ns, key string, now time.Time) (platformObservation, bool) {
	selector := t.Selector
	desired := int32(-1)
	if t.Deployment != "" {
		gctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		dep, err := w.Kube.Clientset.AppsV1().Deployments(ns).Get(gctx, t.Deployment, metav1.GetOptions{})
		cancel()
		if apierrors.IsNotFound(err) {
			return platformObservation{absent: true}, true
		}
		if err != nil {
			w.platformReadErr(ns, err)
			return platformObservation{}, false
		}
		desired = 1
		if dep.Spec.Replicas != nil {
			desired = *dep.Spec.Replicas
		}
		if desired == 0 {
			return platformObservation{absent: true}, true
		}
		if dep.Spec.Selector == nil {
			return platformObservation{}, false
		}
		sel, err := metav1.LabelSelectorAsSelector(dep.Spec.Selector)
		if err != nil {
			return platformObservation{}, false
		}
		selector = sel.String()
	}
	lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	pods, err := w.Kube.Clientset.CoreV1().Pods(ns).List(lctx, metav1.ListOptions{LabelSelector: selector})
	cancel()
	if err != nil {
		w.platformReadErr(ns, err)
		return platformObservation{}, false
	}
	if len(pods.Items) == 0 {
		// A selector-only target with no pods doesn't exist here; a
		// Deployment that wants replicas but has no pods is down.
		if desired < 0 {
			return platformObservation{absent: true}, true
		}
		return platformObservation{unready: true}, true
	}
	var obs platformObservation
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.DeletionTimestamp != nil {
			continue
		}
		if r := podBadReason(p); r != "" && obs.crash == "" {
			obs.crash = r
			if term := lastTermination(p); term != "" {
				obs.crash += " (last exit: " + term + ")"
			}
			obs.crash += " · pod " + p.Name
		}
		if podReady(p) {
			obs.ready = true
		}
	}
	if n := w.recordOOMs(key, pods.Items, now); obs.crash == "" && n >= platformOOMCount {
		obs.crash = fmt.Sprintf("OOMKilled %d times in %s", n, platformOOMWindow)
	}
	obs.unready = !obs.ready
	return obs, true
}

// recordOOMs counts OOM-kill restarts seen since the previous tick and
// returns how many fell inside platformOOMWindow. A pod seen for the
// first time only seeds its restart count: restarts from before this
// process started (or before the pod was first seen) never count.
func (w *Watcher) recordOOMs(key string, pods []corev1.Pod, now time.Time) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.platformRestarts == nil {
		w.platformRestarts = map[string]int32{}
	}
	live := map[string]bool{}
	for i := range pods {
		p := &pods[i]
		for _, cs := range p.Status.ContainerStatuses {
			rk := key + "|" + string(p.UID) + "|" + p.Name + "|" + cs.Name
			live[rk] = true
			prev, seen := w.platformRestarts[rk]
			w.platformRestarts[rk] = cs.RestartCount
			if !seen || cs.RestartCount <= prev {
				continue
			}
			if t := cs.LastTerminationState.Terminated; t != nil && t.Reason == "OOMKilled" {
				for j := int32(0); j < cs.RestartCount-prev; j++ {
					w.platformOOMs[key] = append(w.platformOOMs[key], now)
				}
			}
		}
	}
	for rk := range w.platformRestarts {
		if strings.HasPrefix(rk, key+"|") && !live[rk] {
			delete(w.platformRestarts, rk)
		}
	}
	kept := w.platformOOMs[key][:0]
	for _, t := range w.platformOOMs[key] {
		if now.Sub(t) <= platformOOMWindow {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(w.platformOOMs, key)
		return 0
	}
	w.platformOOMs[key] = kept
	return len(kept)
}

// lastTermination describes a pod's most recent container exit.
func lastTermination(p *corev1.Pod) string {
	for _, cs := range p.Status.ContainerStatuses {
		if t := cs.LastTerminationState.Terminated; t != nil {
			if t.Reason != "" {
				return fmt.Sprintf("%s, code %d", t.Reason, t.ExitCode)
			}
			return fmt.Sprintf("code %d", t.ExitCode)
		}
	}
	return ""
}

// platformReadErr logs a read failure, once per namespace for RBAC
// denials (an install whose kuso-server role can't see that namespace
// would otherwise log every minute forever).
func (w *Watcher) platformReadErr(ns string, err error) {
	if !apierrors.IsForbidden(err) {
		w.Logger.Warn("health: platform workload read", "ns", ns, "err", err)
		return
	}
	w.mu.Lock()
	if w.platformForbidden == nil {
		w.platformForbidden = map[string]bool{}
	}
	first := !w.platformForbidden[ns]
	w.platformForbidden[ns] = true
	w.mu.Unlock()
	if first {
		w.Logger.Warn("health: platform workloads in this namespace are not visible to kuso-server; they are not watched", "ns", ns, "err", err)
	}
}

// platformPersisted maps alerted platform episodes to their start. They
// are persisted so a server roll during an outage neither re-pages nor
// loses the recovery. Caller holds w.mu.
func (w *Watcher) platformPersisted() map[string]time.Time {
	out := map[string]time.Time{}
	for k, ep := range w.platformSeen {
		if ep.alerted {
			out[k] = ep.badSince
		}
	}
	return out
}
