// Package health is the in-cluster watchdog. Every interval it
// surveys node disk usage and pod status across the kuso namespace,
// firing notify events when something crosses a threshold or
// transitions to a bad state.
//
// We deliberately don't use prometheus alertmanager here — the
// install footprint is too big for a single Hetzner box, and the
// signals we care about (disk, crash loops, image pull errors) are
// trivially observable via the kube API. Prometheus stays focused on
// request/error/latency timeseries; this watcher handles operational
// alerts.
//
// We remember which alerts we've already fired so a CrashLoopBackOff
// that lasts an hour doesn't spam Discord every 30s. With a Store the
// open episodes survive a kuso-server roll or lease handover, so an
// upgrade neither re-pages every ongoing crash nor loses the recovery
// for an episode opened by the previous leader.
package health

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/failures"
	"kuso/server/internal/kube"
	"kuso/server/internal/notify"
	"kuso/server/internal/serverstate"
)

// HeartbeatInterval is the watcher's tick cadence (matches New's default
// Interval), exported so main.go can register the health watcher in the
// serverstate liveness registry at the interval it beats.
const HeartbeatInterval = 60 * time.Second

// CrashAlertCooldown is how long a crash key stays remembered after the
// last tick that observed it. A crashlooping container is Running between
// restarts, so a tick can miss it; forgetting the key immediately made
// every such gap re-alert. One alert per episode; an episode ends either
// with a recovery (see RecoveryStableWindow) or, when the env never looks
// healthy again (deleted, scaled to 0, stuck unready), after this long.
const CrashAlertCooldown = 15 * time.Minute

// RecoveryStableWindow is how long a crashed service env must look
// healthy (a Ready pod, no crashing pods, no new restarts) before its
// episode closes with a pod.recovered event. Three 60s ticks: long enough
// that a crashloop's brief Running phase between backoffs doesn't count,
// short enough to beat CrashAlertCooldown by a wide margin.
const RecoveryStableWindow = 3 * time.Minute

// crashEpisode is the per-key state of one alerted crash episode.
type crashEpisode struct {
	since   time.Time // first tick that observed the key bad
	lastBad time.Time // most recent tick that observed it bad

	// Recovery bookkeeping, service keys only (addon episodes just
	// expire). healthySince is the tick the current healthy run began,
	// zero when the env isn't currently healthy. restarts is the env's
	// restart total at the last healthy tick, so a crash-and-restart
	// between two ticks (never seen as Waiting) still resets the window.
	addon                 bool
	project, service, env string
	healthySince          time.Time
	restarts              int
}

// keyObservation aggregates one tick's view of every pod sharing a crash key.
type keyObservation struct {
	badPod   *corev1.Pod
	reason   string
	ready    bool
	restarts int
}

// Watcher polls cluster state every Interval and emits notify events.
// Construct via New, run via Run in a goroutine.
type Watcher struct {
	Kube      *kube.Client
	Namespace string
	Notify    *notify.Dispatcher
	Logger    *slog.Logger
	Interval  time.Duration

	// DiskWarnPct is the node-disk-used % threshold above which we
	// fire alert.fired. Default 85.
	DiskWarnPct int

	mu    sync.Mutex
	fired map[string]bool // node alert key → was already fired
	// crashSeen maps a pod-crash key (see crashKey) to its open episode.
	crashSeen map[string]*crashEpisode

	// Store persists open episodes across restarts. nil keeps them
	// in memory only.
	Store StateStore

	restored  bool
	lastSaved string

	// Test seams; nil means time.Now / w.Notify.Emit.
	now  func() time.Time
	emit func(notify.Event)
}

// StateStore is the Setting key/value surface the watcher persists its
// alert state through (*db.DB satisfies it).
type StateStore interface {
	GetSetting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value, updatedBy string) error
}

// stateKey is the Setting row holding the persisted alert state.
const stateKey = "health.alertState"

type persistedEpisode struct {
	Since        time.Time `json:"since"`
	LastBad      time.Time `json:"lastBad"`
	Addon        bool      `json:"addon,omitempty"`
	Project      string    `json:"project,omitempty"`
	Service      string    `json:"service,omitempty"`
	Env          string    `json:"env,omitempty"`
	HealthySince time.Time `json:"healthySince"`
	Restarts     int       `json:"restarts,omitempty"`
}

type persistedState struct {
	Crash map[string]persistedEpisode `json:"crash"`
	Fired []string                    `json:"fired"`
}

// New returns a Watcher with sensible defaults.
func New(k *kube.Client, ns string, n *notify.Dispatcher, logger *slog.Logger) *Watcher {
	return &Watcher{
		Kube:        k,
		Namespace:   ns,
		Notify:      n,
		Logger:      logger,
		Interval:    HeartbeatInterval,
		DiskWarnPct: 85,
		fired:       map[string]bool{},
		crashSeen:   map[string]*crashEpisode{},
	}
}

func (w *Watcher) clock() time.Time {
	if w.now != nil {
		return w.now()
	}
	return time.Now()
}

func (w *Watcher) send(e notify.Event) {
	if w.emit != nil {
		w.emit(e)
		return
	}
	w.Notify.Emit(e)
}

// Run loops until ctx is cancelled. First tick fires immediately so
// boot-time issues surface within a minute, not after the interval.
func (w *Watcher) Run(ctx context.Context) {
	t := time.NewTicker(w.Interval)
	defer t.Stop()
	w.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.tick(ctx)
			// Liveness heartbeat: the loop completed an iteration. A no-op
			// unless registered (leader-gated in startSingletons).
			serverstate.LoopHeartbeat(serverstate.LoopHealth)
		}
	}
}

func (w *Watcher) tick(ctx context.Context) {
	w.restoreState(ctx)
	w.checkPods(ctx)
	w.checkNodes(ctx)
	w.persistState(ctx)
}

// restoreState loads the previous process's open episodes once, before
// the first check. Entries whose condition cleared while no watcher ran
// close through the normal recovery/cooldown paths on the next ticks.
func (w *Watcher) restoreState(ctx context.Context) {
	if w.Store == nil || w.restored {
		return
	}
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	raw, err := w.Store.GetSetting(rctx, stateKey)
	cancel()
	if err != nil {
		// Retry next tick rather than starting empty and re-paging.
		w.Logger.Warn("health: load alert state", "err", err)
		return
	}
	w.restored = true
	w.lastSaved = raw
	if raw == "" {
		return
	}
	var st persistedState
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		w.Logger.Warn("health: decode alert state; starting empty", "err", err)
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for k, e := range st.Crash {
		if _, ok := w.crashSeen[k]; ok {
			continue
		}
		w.crashSeen[k] = &crashEpisode{
			since: e.Since, lastBad: e.LastBad, addon: e.Addon,
			project: e.Project, service: e.Service, env: e.Env,
			healthySince: e.HealthySince, restarts: e.Restarts,
		}
	}
	for _, k := range st.Fired {
		w.fired[k] = true
	}
}

// persistState writes the alert state when it changed since the last
// write. Failures are logged and retried on the next tick.
func (w *Watcher) persistState(ctx context.Context) {
	if w.Store == nil || !w.restored {
		return
	}
	st := persistedState{Crash: map[string]persistedEpisode{}, Fired: []string{}}
	w.mu.Lock()
	for k, e := range w.crashSeen {
		st.Crash[k] = persistedEpisode{
			Since: e.since, LastBad: e.lastBad, Addon: e.addon,
			Project: e.project, Service: e.service, Env: e.env,
			HealthySince: e.healthySince, Restarts: e.restarts,
		}
	}
	for k := range w.fired {
		st.Fired = append(st.Fired, k)
	}
	w.mu.Unlock()
	sort.Strings(st.Fired)
	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	if string(b) == w.lastSaved {
		return
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := w.Store.SetSetting(sctx, stateKey, string(b), "health-watcher"); err != nil {
		w.Logger.Warn("health: save alert state", "err", err)
		return
	}
	w.lastSaved = string(b)
}

// checkPods finds pods in CrashLoopBackOff / ImagePullBackOff /
// CreateContainerConfigError and fires once per workload episode, keyed
// by (namespace, project, service, env) — not pod name (replicas and
// rollout pods would multiply alerts) and not reason (ErrImagePull and
// ImagePullBackOff alternate between ticks). A service episode closes
// with pod.recovered after RecoveryStableWindow of health; otherwise the
// key is forgotten after CrashAlertCooldown without being observed bad.
func (w *Watcher) checkPods(ctx context.Context) {
	// Only kuso-managed workload pods can be in a state this watcher
	// reports on, so select on the project label rather than listing
	// every pod in the namespace. On a busy cluster the unfiltered list
	// pulled back multi-MB payloads every tick, most of it platform
	// pods this loop then skipped. Mirrors logship's selector.
	//
	// All namespaces: a project may run in its own namespace
	// (kuso-<project>), and listing only the home namespace left every
	// such project without crash alerts.
	pods, err := w.Kube.Clientset.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
		LabelSelector: kube.LabelProject,
	})
	if err != nil {
		w.Logger.Warn("health: list pods", "err", err)
		return
	}
	now := w.clock()

	obs := map[string]*keyObservation{}
	var order []string // first-seen order, so alerts are deterministic
	for i := range pods.Items {
		p := &pods.Items[i]
		// Job pods (crons, runs, builds, seeds, release hooks) aren't
		// long-running workloads: they fail through their own events, and
		// reporting them here filed a cron as a crashing service env.
		if ownedByJob(p) {
			continue
		}
		key := crashKey(p)
		o := obs[key]
		if o == nil {
			o = &keyObservation{}
			obs[key] = o
			order = append(order, key)
		}
		o.restarts += containerRestartTotal(p)
		if reason := podBadReason(p); reason != "" {
			if o.badPod == nil {
				o.badPod, o.reason = p, reason
			}
		} else if podReady(p) {
			o.ready = true
		}
	}

	for _, key := range order {
		o := obs[key]
		if o.badPod == nil {
			continue
		}
		w.mu.Lock()
		ep, open := w.crashSeen[key]
		if open {
			ep.lastBad = now
			ep.healthySince = time.Time{}
		}
		w.mu.Unlock()
		if open {
			continue
		}
		ep = w.alertCrash(ctx, o.badPod, o.reason, now)
		ep.lastBad = now
		w.mu.Lock()
		w.crashSeen[key] = ep
		w.mu.Unlock()
	}

	var recovered []notify.Event
	w.mu.Lock()
	for key, ep := range w.crashSeen {
		o := obs[key]
		if o != nil && o.badPod != nil {
			continue
		}
		// A healthy service env needs a Ready pod: no pods at all means
		// deleted or scaled to 0, which is not a recovery.
		if !ep.addon && o != nil && o.ready {
			if ep.healthySince.IsZero() || o.restarts > ep.restarts {
				ep.healthySince = now
			}
			ep.restarts = o.restarts
			if now.Sub(ep.healthySince) >= RecoveryStableWindow {
				recovered = append(recovered, notify.PodRecovered(ep.project, ep.service, ep.env, ep.healthySince.Sub(ep.since)))
				delete(w.crashSeen, key)
				continue
			}
		} else {
			ep.healthySince = time.Time{}
		}
		if now.Sub(ep.lastBad) > CrashAlertCooldown {
			delete(w.crashSeen, key)
		}
	}
	w.mu.Unlock()
	for _, e := range recovered {
		w.send(e)
	}
}

// alertCrash emits the crash event for the first bad pod of a new
// episode and returns the episode it opens.
func (w *Watcher) alertCrash(ctx context.Context, p *corev1.Pod, reason string, now time.Time) *crashEpisode {
	project := p.Labels[kube.LabelProject]
	restarts := containerRestartTotal(p)
	// Addon pods (postgres/redis/s3/...) carry kuso.sislelabs.com/addon
	// and app.kubernetes.io/name=kusoaddon. They were previously routed
	// through the service crash path below, which read
	// app.kubernetes.io/instance (the addon FQN) as a "service" and
	// emitted a PodCrashed deep-linking to a service overlay that
	// doesn't exist — a phantom service. Route addon crashes to their
	// own higher-severity event (a crashed datastore takes down every
	// service in the project that mounts its conn secret) and skip the
	// service path.
	if addon := p.Labels["kuso.sislelabs.com/addon"]; addon != "" ||
		p.Labels["app.kubernetes.io/name"] == "kusoaddon" {
		if addon == "" {
			addon = p.Labels["app.kubernetes.io/instance"] // fallback to FQN
		}
		addonKind := p.Labels["kuso.sislelabs.com/addon-kind"]
		logTail := lastLines(w.previousLogLines(p, reason, 50), 5)
		shortAddon := strings.TrimPrefix(addon, project+"-")
		w.send(notify.AddonCrashed(project, shortAddon, addonKind, p.Name, reason, logTail, restarts))
		return &crashEpisode{since: now, addon: true, project: project}
	}
	// Pull 50 lines for the classifier; the Discord card still only
	// shows the last 5. The classifier walks the larger window in
	// reverse to find the regex that matches the failure — 5 lines is
	// too small when nixpacks / buildpacks chatter after the actual
	// error. Deriving the tail from the same slice keeps card and
	// classifier agreeing on what "the tail" was.
	logLines := w.previousLogLines(p, reason, 50)
	logTail := lastLines(logLines, 5)
	// Stripping "init:" off the reason for the classifier — the
	// signal taxonomy doesn't distinguish init vs main containers
	// (the user cares about "image-pull failed", not "image pull
	// failed in init container").
	sigReason := strings.TrimPrefix(reason, "init:")
	// Also surface the terminated reason + exit code: a pod that
	// OOMs shows Waiting.Reason=CrashLoopBackOff on restart but
	// Terminated.Reason=OOMKilled / exit=137 on the last run, so
	// classifying on the Waiting reason alone mislabels every OOM
	// as a crashloop. Prefer the terminated signal so Classify
	// reaches KindOOM (and the exit-137 fallback).
	termReason, exitCode := podTerminatedSignal(p)
	sig := failures.Signal{Reason: sigReason, ExitCode: exitCode, Runtime: true}
	if termReason != "" {
		sig.Reason = termReason
	}
	classification := failures.Classify(logLines, sig)
	service := podServiceShort(p)
	envName := w.podEnvName(ctx, p, service)
	w.send(notify.PodCrashed(notify.PodCrash{
		Project:        project,
		Service:        service,
		Env:            envName,
		Pod:            p.Name,
		Reason:         reason,
		LogTail:        logTail,
		Restarts:       restarts,
		Since:          now,
		Classification: &classification,
	}))
	return &crashEpisode{since: now, project: project, service: service, env: envName}
}

func lastLines(lines []string, n int) string {
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// podReady reports a Running pod whose Ready condition is True.
func podReady(p *corev1.Pod) bool {
	if p.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// crashKey identifies the workload a bad pod belongs to. The env
// component is the env CR name (app.kubernetes.io/instance), which is
// unique per (service, env) and shared by every replica and rollout pod.
func crashKey(p *corev1.Pod) string {
	project := p.Labels[kube.LabelProject]
	if addon := p.Labels["kuso.sislelabs.com/addon"]; addon != "" ||
		p.Labels["app.kubernetes.io/name"] == "kusoaddon" {
		if addon == "" {
			addon = p.Labels["app.kubernetes.io/instance"]
		}
		return "addon:" + p.Namespace + "/" + project + "/" + addon
	}
	return "svc:" + p.Namespace + "/" + project + "/" + podServiceShort(p) + "/" + p.Labels["app.kubernetes.io/instance"]
}

// podServiceShort returns the short service slug the web UI uses in
// ?service=. The kusoenvironment chart stamps kuso.sislelabs.com/service
// with the env CR's spec.service, which is the FQN "<project>-<service>";
// app.kubernetes.io/instance is the env CR name ("<fqn>-production"),
// which is why it can't be used directly.
func podServiceShort(p *corev1.Pod) string {
	project := p.Labels[kube.LabelProject]
	svc := p.Labels[kube.LabelService]
	if svc == "" || svc == "unknown" {
		// Pods predating the service label: strip the env suffix off the
		// env CR name to recover the FQN.
		svc = p.Labels["app.kubernetes.io/instance"]
		if i := strings.LastIndex(svc, "-pr-"); i > 0 {
			svc = svc[:i]
		} else {
			svc = strings.TrimSuffix(svc, "-production")
		}
	}
	if project != "" {
		svc = strings.TrimPrefix(svc, project+"-")
	}
	return svc
}

// podEnvName returns the env group name (production / staging /
// preview-pr-7). Pods don't carry kube.LabelEnv — only the env CR does —
// and env-kind on the pod reads "production" for env-group clones like
// staging, so the CR is the authority. Falls back to deriving from the
// env CR name, then to env-kind.
func (w *Watcher) podEnvName(ctx context.Context, p *corev1.Pod, serviceShort string) string {
	if v := p.Labels[kube.LabelEnv]; v != "" {
		return v
	}
	envCR := p.Labels["app.kubernetes.io/instance"]
	if envCR != "" && w.Kube != nil && w.Kube.Dynamic != nil {
		gctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		env, err := w.Kube.GetKusoEnvironment(gctx, p.Namespace, envCR)
		cancel()
		if err == nil && env.Labels[kube.LabelEnv] != "" {
			return env.Labels[kube.LabelEnv]
		}
	}
	prefix := p.Labels[kube.LabelProject] + "-" + serviceShort + "-"
	if suffix, ok := strings.CutPrefix(envCR, prefix); ok && suffix != "" {
		if strings.HasPrefix(suffix, "pr-") {
			return "preview-" + suffix
		}
		return suffix
	}
	return p.Labels["kuso.sislelabs.com/env-kind"]
}

// podTerminatedSignal returns the (reason, exitCode) of the pod's most
// recent abnormal container termination, for feeding failures.Classify.
// OOMKilled lives in State/LastTerminationState.Terminated, NOT in the
// Waiting reason podBadReason reads — so without this an OOM is only ever
// seen as the CrashLoopBackOff it becomes on restart. Checks current then
// last-termination state, main then init containers. ("", 0) when none.
func podTerminatedSignal(p *corev1.Pod) (string, int32) {
	for _, all := range [][]corev1.ContainerStatus{
		p.Status.ContainerStatuses,
		p.Status.InitContainerStatuses,
	} {
		for _, cs := range all {
			t := cs.State.Terminated
			if t == nil {
				t = cs.LastTerminationState.Terminated
			}
			if t == nil || t.ExitCode == 0 {
				continue
			}
			return t.Reason, t.ExitCode
		}
	}
	return "", 0
}

func podBadReason(p *corev1.Pod) string {
	for _, cs := range p.Status.ContainerStatuses {
		if cs.State.Waiting != nil {
			r := cs.State.Waiting.Reason
			if r == "CrashLoopBackOff" || r == "ImagePullBackOff" || r == "CreateContainerConfigError" || r == "ErrImagePull" {
				return r
			}
		}
	}
	for _, cs := range p.Status.InitContainerStatuses {
		if cs.State.Waiting != nil {
			r := cs.State.Waiting.Reason
			if r == "CrashLoopBackOff" || r == "ImagePullBackOff" || r == "CreateContainerConfigError" || r == "ErrImagePull" {
				return "init:" + r
			}
		}
	}
	return ""
}

// checkNodes pulls node usage from metrics-server (already in the
// cluster for the metrics panel) and the node's allocatable storage,
// then alerts when used % is past the threshold. Best-effort: any
// error → skip this tick so a flaky kubelet doesn't spam.
func (w *Watcher) checkNodes(ctx context.Context) {
	// Prefer the shared informer's local view — health.Watcher ticks
	// every 60s and the cluster-wide LIST is ~500ms on a 50-node
	// install. Fall back to a live LIST during the cold-boot sync
	// window.
	var nodeList []*corev1.Node
	if cached, ok := w.Kube.Cache.ListNodes(); ok {
		nodeList = cached
	} else {
		nodes, err := w.Kube.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if err != nil {
			return
		}
		nodeList = make([]*corev1.Node, len(nodes.Items))
		for i := range nodes.Items {
			nodeList[i] = &nodes.Items[i]
		}
	}
	for _, n := range nodeList {
		// We don't have a direct disk-used metric from kube — the
		// metrics-server exposes CPU + mem only. We approximate by
		// reading the node condition list: kubelet sets
		// `DiskPressure=True` when its eviction threshold is hit.
		// That's a coarser signal than a percentage but it's the one
		// that actually matters operationally (eviction is imminent).
		key := "node:" + n.Name + ":disk-pressure"
		pressure := false
		for _, c := range n.Status.Conditions {
			if c.Type == corev1.NodeDiskPressure && c.Status == corev1.ConditionTrue {
				pressure = true
				break
			}
		}
		w.mu.Lock()
		already := w.fired[key]
		if pressure {
			w.fired[key] = true
		} else {
			delete(w.fired, key)
		}
		w.mu.Unlock()
		if pressure && !already {
			w.Notify.Emit(notify.AlertFired(
				"Disk pressure on "+n.Name,
				"kubelet flagged DiskPressure=True. Free up space or pods will start getting evicted.",
				"warn",
				map[string]string{"node": n.Name},
			))
		}
	}
}

// containerRestartTotal sums RestartCount across every container in a
// pod. Init containers are included — an init-loop crash is just as
// alert-worthy as a main-container one, and the user wants the full
// picture in the Discord card.
func containerRestartTotal(p *corev1.Pod) int {
	var total int32
	for _, cs := range p.Status.ContainerStatuses {
		total += cs.RestartCount
	}
	for _, cs := range p.Status.InitContainerStatuses {
		total += cs.RestartCount
	}
	return int(total)
}

// previousLogTail pulls the last ~5 lines of the previous container's
// stdout for a crashing pod. "Previous" matters because the current
// container is in waiting/backoff — its logs are empty; the prior run
// is where the actual error landed.
//
// Best-effort: 5s timeout, kube errors swallowed, empty string when
// no previous run exists (first-boot ImagePullBackOff yields nothing
// to tail anyway). The notify renderer drops empty log tails cleanly.
func (w *Watcher) previousLogTail(p *corev1.Pod, reason string) string {
	lines := w.previousLogLines(p, reason, 5)
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

// previousLogLines is the slice-returning primitive that backs
// previousLogTail and the failure classifier. The classifier walks
// the slice in reverse looking for known regex patterns; the Discord
// card joins the last 5 lines for embedding. Same kube-fetching
// contract: 5s timeout, init-container fallback, empty result on
// any error (the renderer + classifier both tolerate it).
func (w *Watcher) previousLogLines(p *corev1.Pod, reason string, n int) []string {
	if w == nil || w.Kube == nil || p == nil || n <= 0 {
		return nil
	}
	// ImagePullBackOff has no prior container — skip the log read.
	if reason == "ImagePullBackOff" || reason == "ErrImagePull" ||
		reason == "CreateContainerConfigError" || reason == "init:ImagePullBackOff" ||
		reason == "init:ErrImagePull" {
		return nil
	}
	// Find the first container with restart history. Main containers
	// take priority over init containers since runtime crashes are
	// what we usually want to surface.
	cName := ""
	for _, cs := range p.Status.ContainerStatuses {
		if cs.RestartCount > 0 {
			cName = cs.Name
			break
		}
	}
	if cName == "" {
		for _, cs := range p.Status.InitContainerStatuses {
			if cs.RestartCount > 0 {
				cName = cs.Name
				break
			}
		}
	}
	if cName == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tail := int64(n)
	prev := true
	req := w.Kube.Clientset.CoreV1().Pods(p.Namespace).GetLogs(p.Name, &corev1.PodLogOptions{
		Container: cName,
		Previous:  prev,
		TailLines: &tail,
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		return nil
	}
	defer stream.Close()
	buf := make([]byte, 4096)
	var data []byte
	for {
		n, rerr := stream.Read(buf)
		if n > 0 {
			data = append(data, buf[:n]...)
		}
		if rerr != nil {
			break
		}
	}
	s := strings.TrimSpace(string(data))
	if s == "" {
		return nil
	}
	ls := strings.Split(s, "\n")
	if len(ls) > n {
		ls = ls[len(ls)-n:]
	}
	return ls
}

func ownedByJob(p *corev1.Pod) bool {
	for _, o := range p.OwnerReferences {
		if o.Kind == "Job" {
			return true
		}
	}
	return false
}

// PodBadReason exposes the crash-alert classification: non-empty when
// the pod is in a state pod.crashed alerts on.
func PodBadReason(p *corev1.Pod) string { return podBadReason(p) }
