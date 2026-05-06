// Package logship streams pod logs into the SQLite LogLine table for
// search + alerting. Watches every pod in the kuso namespace; opens
// a follow stream per pod; batches inserts every 1s or 500 lines.
//
// Retention: 14 days, pruned on a slow ticker (every 30 min).
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
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/db"
	"kuso/server/internal/kube"
)

const (
	// Retention window. Older lines get pruned.
	Retention = 14 * 24 * time.Hour
	// How often we list pods to reconcile follow streams.
	pollInterval = 30 * time.Second
	// Flush batch buffer this often or when len ≥ flushBatchSize.
	flushInterval  = 1 * time.Second
	flushBatchSize = 500
	// Max line length we store. Past this we truncate. App logs that
	// dump a 5MB JSON in one line shouldn't blow up FTS5.
	maxLineLen = 16 * 1024
)

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

	mu      sync.Mutex
	streams map[string]context.CancelFunc // podUID → cancel
	buf     []db.LogLine
	bufMu   sync.Mutex

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
	return &Shipper{
		DB: d, Kube: k, Namespace: namespace, Logger: logger,
		streams: map[string]context.CancelFunc{},
	}
}

// Run blocks until ctx done. Idempotent: re-running picks up state
// from existing pods (every stream restarts from a fresh tail).
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
	s.Logger.Info("logship starting", "namespace", s.Namespace, "retention", Retention)

	// Periodic flusher — drain the buffer every flushInterval so
	// lines hit SQLite without us waiting for a 500-line batch from
	// a quiet service.
	go s.runFlusher(ctx)
	// Periodic pruner — drop rows past retention. 30 min ticker
	// keeps the table bounded without hammering DELETE.
	go s.runPruner(ctx)

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
		}
	}
}

func (s *Shipper) reconcilePods(ctx context.Context) {
	listCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	pods, err := s.Kube.Clientset.CoreV1().Pods(s.Namespace).List(listCtx, metav1.ListOptions{})
	if err != nil {
		s.Logger.Warn("logship list pods", "err", err)
		return
	}
	seen := map[string]struct{}{}
	for i := range pods.Items {
		p := &pods.Items[i]
		uid := string(p.UID)
		seen[uid] = struct{}{}
		if p.Status.Phase != corev1.PodRunning && p.Status.Phase != corev1.PodSucceeded {
			continue
		}
		s.mu.Lock()
		_, has := s.streams[uid]
		s.mu.Unlock()
		if has {
			continue
		}
		streamCtx, cancel := context.WithCancel(ctx)
		s.mu.Lock()
		s.streams[uid] = cancel
		s.mu.Unlock()
		go s.streamPod(streamCtx, *p)
	}
	// Drop streams for vanished pods.
	s.mu.Lock()
	for uid, cancel := range s.streams {
		if _, ok := seen[uid]; !ok {
			cancel()
			delete(s.streams, uid)
		}
	}
	s.mu.Unlock()
}

func (s *Shipper) streamPod(ctx context.Context, pod corev1.Pod) {
	defer func() {
		s.mu.Lock()
		delete(s.streams, string(pod.UID))
		s.mu.Unlock()
	}()
	// Tail starting from "now"-ish: 100 lines back. Hot pods that
	// produce thousands per second wouldn't want full historical
	// replay; new pods get full output by virtue of TailLines being
	// soft-capped by what the kubelet still has.
	tail := int64(100)
	req := s.Kube.Clientset.CoreV1().Pods(s.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
		Follow:     true,
		TailLines:  &tail,
		Timestamps: false,
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		s.Logger.Debug("logship stream open", "pod", pod.Name, "err", err)
		return
	}
	defer stream.Close()

	// Pull project / service / env labels off the pod for metadata.
	project := pod.Labels["kuso.sislelabs.com/project"]
	service := pod.Labels["kuso.sislelabs.com/service"]
	env := pod.Labels["kuso.sislelabs.com/env"]

	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
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

func (s *Shipper) append(l db.LogLine) {
	s.bufMu.Lock()
	s.buf = append(s.buf, l)
	shouldFlush := len(s.buf) >= flushBatchSize
	s.bufMu.Unlock()
	if shouldFlush {
		// Out-of-band flush so a single noisy pod doesn't gate the
		// rest of the system on the timed flush. Use the shipper's
		// lifecycle ctx so this goroutine cancels cleanly on shutdown
		// and doesn't race the graceful flush in Run's select loop.
		ctx := s.runCtx
		if ctx == nil {
			// append called before Run set runCtx — shouldn't happen,
			// but fall back to a short bounded context rather than
			// the unbounded context.Background() (which prevented
			// shutdown from interrupting an in-flight flush).
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
			go func() { defer cancel(); s.flush(ctx) }()
			return
		}
		go s.flush(ctx)
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
	n, err := s.DB.PruneLogsOlderThan(pctx, time.Now().Add(-Retention))
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
