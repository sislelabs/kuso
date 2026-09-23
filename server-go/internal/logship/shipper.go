// Package logship streams pod logs into the SQLite LogLine table for
// search + alerting. Watches every pod in the kuso namespace; opens
// a follow stream per pod; batches inserts every 1s or 500 lines.
//
// Retention: 7 days by default (KUSO_LOG_RETENTION_DAYS overrides),
// pruned on a slow ticker (every 30 min).
//
// Why not Loki / Vector / ClickHouse: kuso's deployment shape is one
// SQLite file on the control plane. Adding a stateful third party
// to the indie SaaS happy-path doubles the install complexity.
// SQLite FTS5 over 14d × ~1k lines/min × N pods is comfortably under
// 1GB; small clusters run at ~50MB. When the user outgrows it they
// can swap to a real log backend.
package logship

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/db"
	"kuso/server/internal/kube"
	"kuso/server/internal/serverstate"
)

const (
	// Retention is the default log-line retention window. Lowered from
	// 14d to 7d in v0.18.x: on a live cluster the LogLine table + its
	// pg_trgm GIN index reached ~2.7GB (index > data), and observed data
	// only ever spanned 7 days of active use. Halving the window halves
	// the table AND every index on it. Operators who need a longer
	// window override with KUSO_LOG_RETENTION_DAYS (see resolveRetention).
	Retention = 7 * 24 * time.Hour
	// How often we list pods to reconcile follow streams.
	pollInterval = 30 * time.Second
	// HeartbeatInterval is pollInterval, exported so main.go can register
	// the shipper in the serverstate liveness registry at the cadence its
	// main loop actually beats. Kept in lockstep with pollInterval.
	HeartbeatInterval = pollInterval
	// Flush batch buffer this often or when len ≥ flushBatchSize.
	flushInterval  = 1 * time.Second
	flushBatchSize = 500
	// Max line length we store. Past this we truncate. App logs that
	// dump a 5MB JSON in one line shouldn't blow up FTS5.
	maxLineLen = 16 * 1024

	// rateWindow / rateMaxLinesPerService bound how many lines a single
	// (project, service) can write to LogLine per window. A service that
	// exceeds the cap has its excess lines dropped for the rest of the
	// window with a single warn log — one runaway app can't crowd out
	// everyone else's logs or bloat the control-plane DB. 6000 lines/min
	// (~100/s sustained) is generous for any real service; genuine
	// high-volume needs a real log backend, which is the documented
	// escape hatch. Override the cap with KUSO_LOG_MAX_LINES_PER_MIN.
	rateWindow             = 1 * time.Minute
	rateMaxLinesPerService = 6000

	// firstTailLines bounds the first stream of a container with no
	// resume point (new pod, or a restart with nothing stored for it).
	firstTailLines = 100
	// resumeOverlap rewinds a resume point seeded from the DB. Stored ts
	// is ingest time, and the previous leader may have died holding an
	// unflushed buffer: a few duplicate lines beat a gap.
	resumeOverlap              = 2 * time.Second
	defaultContainerAnnotation = "kubectl.kubernetes.io/default-container"
)

// resolveRateCap returns the per-service per-window line cap:
// KUSO_LOG_MAX_LINES_PER_MIN (must be > 0) if set, else the default.
// A value of 0 or negative, or an unparseable value, falls back to the
// default rather than disabling the cap (fail safe, not open).
func resolveRateCap() int {
	v := strings.TrimSpace(os.Getenv("KUSO_LOG_MAX_LINES_PER_MIN"))
	if v == "" {
		return rateMaxLinesPerService
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return rateMaxLinesPerService
	}
	return n
}

// resolveRetention returns the active retention window: KUSO_LOG_RETENTION_DAYS
// (clamped to 1..90) if set and parseable, else the Retention default.
// Kept out of a package var so tests and boot logging see the same value.
func resolveRetention() time.Duration {
	v := strings.TrimSpace(os.Getenv("KUSO_LOG_RETENTION_DAYS"))
	if v == "" {
		return Retention
	}
	days, err := strconv.Atoi(v)
	if err != nil || days < 1 {
		return Retention
	}
	if days > 90 {
		days = 90
	}
	return time.Duration(days) * 24 * time.Hour
}

// Shipper is the goroutine. Construct via New, call Run with a
// cancellable context.
//
// As of v0.7.17 the shipper writes to a dedicated *db.LogDB instead
// of the main *db.DB. Splitting the storage decouples the heaviest
// writer in the system (FTS5-amplified log batches every 1s) from
// the latency-sensitive control plane (auth, audit, notifications,
// node metrics) — they used to share the single SQLite write
// connection.
type Shipper struct {
	DB        *db.LogDB
	Kube      *kube.Client
	Namespace string
	Logger    *slog.Logger

	mu         sync.Mutex
	containers map[string]*containerState // ns/podUID/container
	buf        []db.LogLine
	bufMu      sync.Mutex

	// Seams for tests; New wires them to the clientset and the DB.
	openLogs     func(ctx context.Context, ns, pod string, opts *corev1.PodLogOptions) (io.ReadCloser, error)
	lastStoredTs func(ctx context.Context, project, service, pod string, since time.Time) (time.Time, error)

	// flushing is a single-flight guard for the out-of-band flush that
	// append() kicks off when the buffer crosses flushBatchSize.
	//
	// Without it, EVERY append past the threshold spawned a new
	// goroutine. A burst of log lines (a crash-looping service, a
	// verbose worker) therefore spawned flushes far faster than they
	// complete: each one holds a Postgres connection for up to the 10s
	// InsertLogLines timeout, so a few hundred of them exhaust the
	// 25-connection pool (db.go:61) and starve every other query in
	// the server — API requests included. Only one out-of-band flush
	// may be in flight; the timed flusher still runs on its own
	// cadence, and a skipped burst-flush is picked up either by the
	// next append or by that ticker.
	flushing atomic.Bool

	// rate caps per-service log ingestion so one chatty service can't
	// dominate the shared LogLine table (observed: a single worker
	// produced 42% of all lines cluster-wide). Keyed by "project/service",
	// counts lines accepted in the current window; reset every
	// rateWindow by resetRateCounters. Guarded by rateMu.
	rateMu      sync.Mutex
	rateCounts  map[string]int
	rateDropped map[string]int // lines dropped this window (for the warn log)

	// envHints accumulates "missing env var" hits parsed out of pod
	// stdout. Keyed by "project/service/name" for natural dedupe
	// against a hot crash-loop. Drained by the same flusher that
	// writes log lines, so the persistence latency is bounded by
	// flushInterval.
	envHintsMu sync.Mutex
	envHints   map[string]envHint

	// runCtx is the lifecycle context set by Run. Detached out-of-band
	// flushes (kicked from append() when the buffer exceeds the batch
	// threshold) use this so they get cancelled on shutdown — the
	// previous code passed context.Background() and those goroutines
	// kept running against a closed DB after Run returned, racing
	// the graceful flush in the Run select loop.
	runCtx context.Context
}

func New(d *db.LogDB, k *kube.Client, namespace string, logger *slog.Logger) *Shipper {
	if namespace == "" {
		namespace = "kuso"
	}
	if logger == nil {
		logger = slog.Default()
	}
	s := &Shipper{
		DB: d, Kube: k, Namespace: namespace, Logger: logger,
		containers: map[string]*containerState{},
		// Pre-seed runCtx so append() called before Run() doesn't fall
		// back to context.Background() (which would spawn an
		// uncancellable flush goroutine). Run() overrides this with
		// the real lifecycle context; until then the bounded background
		// keeps the contract honest.
		runCtx: context.Background(),
	}
	s.openLogs = func(ctx context.Context, ns, pod string, opts *corev1.PodLogOptions) (io.ReadCloser, error) {
		return s.Kube.Clientset.CoreV1().Pods(ns).GetLogs(pod, opts).Stream(ctx)
	}
	s.lastStoredTs = func(ctx context.Context, project, service, pod string, since time.Time) (time.Time, error) {
		return s.DB.LatestPodLogTs(ctx, project, service, pod, since)
	}
	return s
}

// containerState is the shipping cursor for one container of one pod.
// A terminated container is streamed to EOF once and then never
// reopened; a running one resumes from lastTs instead of re-tailing.
type containerState struct {
	streaming bool
	cancel    context.CancelFunc
	// seeded: lastTs has been resolved (from a stream or the DB), so a
	// zero lastTs really means "nothing shipped yet".
	seeded  bool
	lastTs  time.Time // kubelet timestamp of the newest shipped line
	nAtLast int       // lines already shipped carrying exactly lastTs
	// doneRestart is the restart count whose terminated instance was
	// shipped to EOF; -1 when none.
	doneRestart int32
}

// Run blocks until ctx done. After a restart or leader failover each
// container resumes from the newest line stored for its pod, or from a
// bounded tail when there is none.
func (s *Shipper) Run(ctx context.Context) {
	if s.Kube == nil || s.Kube.Clientset == nil {
		s.Logger.Warn("logship: kube client unavailable, log shipping disabled")
		return
	}
	if s.DB == nil {
		s.Logger.Warn("logship: log DB unavailable, log shipping disabled")
		return
	}
	s.runCtx = ctx
	s.Logger.Info("logship starting", "namespace", s.Namespace, "retention", resolveRetention())

	// Periodic flusher — drain the buffer every flushInterval so
	// lines hit SQLite without us waiting for a 500-line batch from
	// a quiet service.
	go s.runFlusher(ctx)
	// Periodic pruner — drop rows past retention. 30 min ticker
	// keeps the table bounded without hammering DELETE.
	go s.runPruner(ctx)
	// Per-service rate-cap counter reset — every rateWindow, zero the
	// counters and warn about any service that got throttled.
	go s.resetRateCounters(ctx)

	// Pod watcher — list pods on a slow ticker, start follow
	// streams for new ones, drop streams for vanished pods.
	t := time.NewTicker(pollInterval)
	defer t.Stop()
	s.reconcilePods(ctx)
	for {
		select {
		case <-ctx.Done():
			s.Logger.Info("logship stopping")
			s.flush(ctx)
			return
		case <-t.C:
			s.reconcilePods(ctx)
			// Heartbeat off the pod-reconcile ticker (pollInterval, ~30s).
			// The buffer flusher / pruner / rate-reset run as separate
			// goroutines with their own cadences; this main loop's tick is
			// the representative "logship is alive" signal. Register logship
			// at pollInterval to match.
			serverstate.LoopHeartbeat(serverstate.LoopLogship)
		}
	}
}

func (s *Shipper) reconcilePods(ctx context.Context) {
	for _, ns := range s.scanNamespaces(ctx) {
		s.reconcileNamespacePods(ctx, ns)
	}
}

func (s *Shipper) scanNamespaces(ctx context.Context) []string {
	out := []string{s.Namespace}
	seen := map[string]struct{}{s.Namespace: {}}
	if s.Kube == nil || s.Kube.Dynamic == nil {
		return out
	}
	listCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// Go through ListKusoProjects rather than the dynamic client
	// directly: that helper serves from the informer cache when it is
	// synced and falls back to a live LIST when it isn't. The raw
	// Dynamic call here bypassed the cache entirely and hand-decoded
	// the same objects, so this 30s loop paid a live apiserver LIST
	// forever for data already resident in memory.
	projects, err := s.Kube.ListKusoProjects(listCtx, s.Namespace)
	if err != nil {
		s.Logger.Warn("logship list projects for namespaces", "err", err)
		return out
	}
	for i := range projects {
		ns := projects[i].Spec.Namespace
		if ns == "" {
			ns = s.Namespace
		}
		if _, ok := seen[ns]; ok {
			continue
		}
		seen[ns] = struct{}{}
		out = append(out, ns)
	}
	return out
}

func (s *Shipper) reconcileNamespacePods(ctx context.Context, ns string) {
	listCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// Only list kuso-owned pods (kuso.sislelabs.com/project=...).
	// Previously listed every pod in the namespace which dragged in
	// ingress controllers, cert-manager, monitoring, etc. — all of
	// whose logs we never stream — every 30s. The label-exists
	// selector keeps the response bounded to workloads we care about.
	pods, err := s.Kube.Clientset.CoreV1().Pods(ns).List(listCtx, metav1.ListOptions{
		LabelSelector: kube.LabelProject,
	})
	if err != nil {
		s.Logger.Warn("logship list pods", "namespace", ns, "err", err)
		return
	}
	seen := map[string]struct{}{}
	for i := range pods.Items {
		p := &pods.Items[i]
		podKey := ns + "/" + string(p.UID)
		seen[podKey] = struct{}{}
		switch p.Status.Phase {
		case corev1.PodRunning, corev1.PodSucceeded, corev1.PodFailed:
		default:
			continue
		}
		container := logContainer(p)
		if container == "" {
			continue
		}
		terminated, restarts := containerTerminated(p, container)
		key := podKey + "/" + container
		s.mu.Lock()
		st := s.containers[key]
		if st == nil {
			st = &containerState{doneRestart: -1}
			s.containers[key] = st
		}
		if st.streaming || (terminated && st.doneRestart == restarts) {
			s.mu.Unlock()
			continue
		}
		streamCtx, cancel := context.WithCancel(ctx)
		st.streaming, st.cancel = true, cancel
		s.mu.Unlock()
		go s.streamContainer(streamCtx, ns, *p, container, st, terminated, restarts)
	}
	// Drop state (and any stream) for vanished pods.
	s.mu.Lock()
	for key, st := range s.containers {
		if !strings.HasPrefix(key, ns+"/") {
			continue
		}
		if _, ok := seen[key[:strings.LastIndex(key, "/")]]; !ok {
			if st.cancel != nil {
				st.cancel()
			}
			delete(s.containers, key)
		}
	}
	s.mu.Unlock()
}

// logContainer picks the container the API server would pick for a
// GetLogs call without a container name; "" when it would refuse.
func logContainer(p *corev1.Pod) string {
	if len(p.Spec.Containers) == 1 {
		return p.Spec.Containers[0].Name
	}
	return p.Annotations[defaultContainerAnnotation]
}

// containerTerminated reports whether the container's current instance
// has exited (so its log is final) and that instance's restart count.
func containerTerminated(p *corev1.Pod, container string) (bool, int32) {
	for _, cs := range p.Status.ContainerStatuses {
		if cs.Name == container {
			return cs.State.Terminated != nil, cs.RestartCount
		}
	}
	return p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed, 0
}

func (s *Shipper) streamContainer(ctx context.Context, ns string, pod corev1.Pod, container string, st *containerState, terminated bool, restarts int32) {
	defer func() {
		s.mu.Lock()
		st.streaming, st.cancel = false, nil
		s.mu.Unlock()
	}()

	// Pull project / service / env labels off the pod for metadata.
	project := pod.Labels["kuso.sislelabs.com/project"]
	service := pod.Labels["kuso.sislelabs.com/service"]
	env := pod.Labels["kuso.sislelabs.com/env"]

	s.mu.Lock()
	seeded, lastTs, nAtLast := st.seeded, st.lastTs, st.nAtLast
	s.mu.Unlock()
	if !seeded {
		// No in-memory cursor: first sight of this container, or a
		// fresh leader. Resume after what an earlier run stored.
		stored, err := s.lastStoredTs(ctx, project, service, pod.Name, pod.CreationTimestamp.Time)
		if err != nil {
			// Fall back to a bounded tail rather than skip the pod.
			s.Logger.Debug("logship resume point", "pod", pod.Name, "err", err)
		} else if !stored.IsZero() {
			lastTs, nAtLast = stored.Add(-resumeOverlap), 0
		}
		s.mu.Lock()
		st.seeded, st.lastTs, st.nAtLast = true, lastTs, nAtLast
		s.mu.Unlock()
	}

	opts := &corev1.PodLogOptions{Container: container, Follow: true, Timestamps: true}
	if lastTs.IsZero() {
		tail := int64(firstTailLines)
		opts.TailLines = &tail
	} else {
		// SinceTime travels at second precision, so the kubelet re-serves
		// the rest of lastTs's second; the loop below drops those lines.
		since := metav1.NewTime(lastTs)
		opts.SinceTime = &since
	}
	stream, err := s.openLogs(ctx, ns, pod.Name, opts)
	if err != nil {
		s.Logger.Debug("logship stream open", "pod", pod.Name, "err", err)
		return
	}
	defer stream.Close()

	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	skippedAtLast := 0
	for scanner.Scan() {
		ts, line, ok := splitTimestamp(scanner.Text())
		if ok {
			if ts.Before(lastTs) {
				continue
			}
			if ts.Equal(lastTs) && skippedAtLast < nAtLast {
				skippedAtLast++
				continue
			}
			if ts.Equal(lastTs) {
				nAtLast++
			} else {
				lastTs, nAtLast = ts, 1
			}
			s.mu.Lock()
			st.lastTs, st.nAtLast = lastTs, nAtLast
			s.mu.Unlock()
		}
		if line == "" {
			continue
		}
		if len(line) > maxLineLen {
			line = line[:maxLineLen] + "…[truncated]"
		}
		s.append(db.LogLine{
			Ts: time.Now().UTC(), Pod: pod.Name,
			Project: project, Service: service, Env: env,
			Line: line,
		})
		// Pattern-match for missing-env-var crashes. Cheap regex
		// per line; on hit we record the var name + log line so the
		// UI can surface "your last crash mentioned $X — set it?"
		// next to the EnvVarsEditor. Async via the shipper's existing
		// goroutine so we don't block the log path.
		if name := matchMissingEnv(line); name != "" && project != "" && service != "" {
			s.recordEnvHint(project, service, name, line)
		}
	}
	// A terminated container's log is final: once read to EOF it is
	// never reopened. A read error other than an over-long line means
	// we may have stopped early, so the next tick resumes instead.
	if err := scanner.Err(); ctx.Err() == nil && terminated && (err == nil || errors.Is(err, bufio.ErrTooLong)) {
		s.mu.Lock()
		st.doneRestart = restarts
		s.mu.Unlock()
	}
}

// splitTimestamp splits a Timestamps:true kubelet line into its
// RFC3339Nano prefix and the message. ok is false when there is no
// parseable prefix; the whole input is then returned as the message.
func splitTimestamp(raw string) (time.Time, string, bool) {
	i := strings.IndexByte(raw, ' ')
	if i <= 0 {
		if ts, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			return ts, "", true
		}
		return time.Time{}, raw, false
	}
	ts, err := time.Parse(time.RFC3339Nano, raw[:i])
	if err != nil {
		return time.Time{}, raw, false
	}
	return ts, raw[i+1:], true
}

// missingEnvPatterns capture the most common framework messages for
// "this env var is unset". Each must yield the var name in capture
// group 1. Keep the list short — one bad regex burns a per-line CPU
// cost on every pod's stdout.
var missingEnvPatterns = []*regexp.Regexp{
	// Python: KeyError: 'FOO'  /  KeyError: "FOO"
	regexp.MustCompile(`KeyError: ['"]([A-Z][A-Z0-9_]+)['"]`),
	// Node: ReferenceError: FOO is not defined  (rare but real)
	regexp.MustCompile(`ReferenceError: ([A-Z][A-Z0-9_]+) is not defined`),
	// dotenv-style: Missing env var FOO / Missing env: FOO / Required env var FOO
	regexp.MustCompile(`(?:Missing|Required) env(?:\s*var)?[:\s]+([A-Z][A-Z0-9_]+)`),
	// Go: panic: missing FOO env var
	regexp.MustCompile(`(?:panic|fatal):.*missing\s+([A-Z][A-Z0-9_]+)\s+env`),
	// envconfig (Go): required key FOO missing value
	regexp.MustCompile(`required key ([A-Z][A-Z0-9_]+) missing value`),
	// generic: Environment variable FOO is not set / FOO is required but not set
	regexp.MustCompile(`(?:Environment variable\s+)?([A-Z][A-Z0-9_]+)\s+is (?:required|not set)`),
}

// matchMissingEnv tries every pattern, returns the first var name
// captured or "" when nothing matches.
func matchMissingEnv(line string) string {
	if len(line) < 8 {
		return ""
	}
	for _, re := range missingEnvPatterns {
		m := re.FindStringSubmatch(line)
		if len(m) >= 2 {
			return m[1]
		}
	}
	return ""
}

// recordEnvHint stamps the (project, service, var-name) tuple onto an
// in-memory map. The shipper's flusher persists it to the DB along
// with the other log lines. Dedupe by (proj/svc/name) so a hot crash-
// loop that emits the same line 1000×/sec doesn't pile up rows.
func (s *Shipper) recordEnvHint(project, service, name, line string) {
	s.envHintsMu.Lock()
	defer s.envHintsMu.Unlock()
	if s.envHints == nil {
		s.envHints = map[string]envHint{}
	}
	key := project + "/" + service + "/" + name
	s.envHints[key] = envHint{
		Project:  project,
		Service:  service,
		Name:     name,
		LastLine: line,
		LastSeen: time.Now().UTC(),
	}
}

// envHint is the in-memory shape of a missing-env detection. Persisted
// by the flusher (see runFlusher) into the EnvHint table.
type envHint struct {
	Project  string
	Service  string
	Name     string
	LastLine string
	LastSeen time.Time
}

// flushEnvHints drains the in-memory map into the EnvHint table.
// Cheap upsert (UNIQUE constraint on project/service/name); a hot
// crashloop emitting the same line repeatedly produces O(1) DB writes
// per flush window per (proj, svc, name) tuple.
func (s *Shipper) flushEnvHints(ctx context.Context) {
	s.envHintsMu.Lock()
	if len(s.envHints) == 0 {
		s.envHintsMu.Unlock()
		return
	}
	hints := make([]db.EnvHint, 0, len(s.envHints))
	for _, h := range s.envHints {
		hints = append(hints, db.EnvHint{
			Project:  h.Project,
			Service:  h.Service,
			Name:     h.Name,
			LastLine: h.LastLine,
			LastSeen: h.LastSeen,
		})
	}
	s.envHints = nil
	s.envHintsMu.Unlock()
	hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.DB.UpsertEnvHints(hctx, hints); err != nil {
		s.Logger.Warn("logship env hints upsert", "n", len(hints), "err", err)
	}
}

// allowLine enforces the per-service rate cap. Returns false when the
// (project, service) has already hit the cap this window, in which case
// the caller drops the line. Lines with no service label are never
// capped (system/unlabelled pods are low-volume and we don't want to
// silently lose their crash output).
func (s *Shipper) allowLine(project, service string) bool {
	if service == "" {
		return true
	}
	key := project + "/" + service
	cap := resolveRateCap()
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	if s.rateCounts == nil {
		s.rateCounts = map[string]int{}
	}
	if s.rateCounts[key] >= cap {
		if s.rateDropped == nil {
			s.rateDropped = map[string]int{}
		}
		s.rateDropped[key]++
		return false
	}
	s.rateCounts[key]++
	return true
}

// resetRateCounters zeroes the per-service counters every rateWindow and
// emits one warn per service that hit the cap, so operators can see which
// app is being throttled without per-line spam.
func (s *Shipper) resetRateCounters(ctx context.Context) {
	t := time.NewTicker(rateWindow)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.rateMu.Lock()
			for key, n := range s.rateDropped {
				if n > 0 {
					s.Logger.Warn("logship: per-service log rate cap hit, dropping excess",
						"service", key, "dropped", n, "cap", resolveRateCap(), "window", rateWindow)
				}
			}
			s.rateCounts = map[string]int{}
			s.rateDropped = map[string]int{}
			s.rateMu.Unlock()
		}
	}
}

func (s *Shipper) append(l db.LogLine) {
	if !s.allowLine(l.Project, l.Service) {
		return
	}
	s.bufMu.Lock()
	s.buf = append(s.buf, l)
	shouldFlush := len(s.buf) >= flushBatchSize
	s.bufMu.Unlock()
	if shouldFlush {
		// Out-of-band flush so a single noisy pod doesn't gate the
		// rest of the system on the timed flush. runCtx is seeded in
		// New() and replaced by Run(), so it's never nil here.
		//
		// Single-flight: see the `flushing` field. Losing the CAS means
		// a flush is already draining the buffer, so this batch is
		// already covered — spawning another would only add a second
		// goroutine contending for the same connection pool.
		if s.flushing.CompareAndSwap(false, true) {
			go func() {
				defer s.flushing.Store(false)
				s.flush(s.runCtx)
			}()
		}
	}
}

func (s *Shipper) runFlusher(ctx context.Context) {
	t := time.NewTicker(flushInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.flush(ctx)
		}
	}
}

func (s *Shipper) flush(ctx context.Context) {
	// Drain env hints first; the path is fast and lets the UI surface
	// a crash hint before the bulk log batch lands.
	s.flushEnvHints(ctx)
	s.bufMu.Lock()
	if len(s.buf) == 0 {
		s.bufMu.Unlock()
		return
	}
	batch := s.buf
	s.buf = nil
	s.bufMu.Unlock()
	flushCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := s.DB.InsertLogLines(flushCtx, batch); err != nil {
		s.Logger.Warn("logship flush", "lines", len(batch), "err", err)
		// Re-queue the lost batch so transient SQLite contention
		// doesn't drop logs. Cap at 10× the batch size to avoid an
		// unbounded buffer when the DB is genuinely down.
		s.bufMu.Lock()
		if len(s.buf)+len(batch) <= flushBatchSize*10 {
			s.buf = append(batch, s.buf...)
		}
		s.bufMu.Unlock()
	}
}

func (s *Shipper) runPruner(ctx context.Context) {
	// Run once shortly after start so a freshly-restarted server
	// trims any backlog accumulated while it was off.
	pruneAfter := time.NewTimer(2 * time.Minute)
	defer pruneAfter.Stop()
	t := time.NewTicker(30 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-pruneAfter.C:
			s.prune(ctx)
		case <-t.C:
			s.prune(ctx)
		}
	}
}

func (s *Shipper) prune(ctx context.Context) {
	pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	n, err := s.DB.PruneLogsOlderThan(pctx, time.Now().Add(-resolveRetention()))
	if err != nil {
		s.Logger.Warn("logship prune", "err", err)
		return
	}
	if n > 0 {
		s.Logger.Debug("logship pruned", "rows", n)
	}
}

// PodMetaForPod is a small helper exposing the label conventions to
// other packages so they don't reimplement the lookup. Trim path —
// not used inside this package but useful for logs handler future
// extensions.
func PodMetaForPod(p *corev1.Pod) (project, service, env string) {
	if p == nil {
		return "", "", ""
	}
	return p.Labels["kuso.sislelabs.com/project"],
		p.Labels["kuso.sislelabs.com/service"],
		p.Labels["kuso.sislelabs.com/env"]
}

// formatTs is a no-op kept for forward compat — the search endpoint
// formats timestamps client-side now. Kept exported so the alerts
// package can pin to the same RFC3339 pattern when expanding.
func formatTs(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// Verify the package can resolve strings.HasPrefix usage from helpers
// the alert engine adds later.
var _ = strings.HasPrefix
var _ = formatTs
