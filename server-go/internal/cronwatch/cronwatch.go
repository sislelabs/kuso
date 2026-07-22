// Package cronwatch detects failed scheduled jobs (KusoCron-owned)
// and dispatches them via the per-cron onFailure webhook + the
// shared notify dispatcher.
//
// Loop:
//   - Every Tick (default 30s), list every Job in every namespace
//     labeled kuso.sislelabs.com/cron.
//   - For Jobs in terminal Failed state we haven't seen before,
//     resolve the parent KusoCron and dispatch.
//   - Idempotency: in-memory map of dispatched Job UIDs. Each tick
//     prunes UIDs that no longer match a live Job, so the map stays
//     bounded by the current cluster Job count even on a long-running
//     server.
//
// Why a separate package: notify already routes events, but the
// detection (watch Jobs → resolve KusoCron → render payload + HMAC) is
// independent enough to deserve its own loop with its own knobs.
// Mirrors nodewatch.Watcher's shape.
package cronwatch

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/httpx"
	"kuso/server/internal/kube"
	"kuso/server/internal/notify"
)

// Config tunes the loop. Zero values fall back to defaults.
type Config struct {
	Tick time.Duration
	// HTTPTimeout caps the outbound webhook call. Defaults to 5s —
	// long enough for a Slack/Discord webhook to ack, short enough
	// that a misbehaving endpoint doesn't pile up.
	HTTPTimeout time.Duration
}

func (c Config) tick() time.Duration {
	if c.Tick <= 0 {
		return 30 * time.Second
	}
	return c.Tick
}

func (c Config) httpTimeout() time.Duration {
	if c.HTTPTimeout <= 0 {
		return 5 * time.Second
	}
	return c.HTTPTimeout
}

// KubeTimeout caps each apiserver call the watcher makes (Job list,
// GetKusoCron, signing-secret read). The kube REST client sets no
// global timeout, so without this a hung apiserver would block the
// tick goroutine indefinitely and silently stall cron-failure alerting.
// Defaults to 15s, matching nodewatch.
func (c Config) kubeTimeout() time.Duration {
	return 15 * time.Second
}

// Watcher polls Jobs labeled kuso.sislelabs.com/cron and fires
// notify events + webhook calls when one terminates in Failed state.
type Watcher struct {
	Kube   *kube.Client
	Notify *notify.Dispatcher
	Logger *slog.Logger
	Config Config
	// BaseURL is the public origin of this kuso instance (e.g.
	// https://kuso.tickero.bg). Used to render logsURL in the payload
	// so the recipient has a deep-link. Empty = omit the field.
	BaseURL string
	// HTTP overrides the default http.Client for webhook delivery.
	// Tests inject a stub; production leaves it nil.
	HTTP *http.Client

	mu sync.Mutex
	// dispatched records Job UIDs we've already fired for, so a
	// failed Job that sticks around (failedJobsHistoryLimit > 0)
	// doesn't re-fire on every tick. Pruned each tick against the live
	// Job set (see tick) so it doesn't grow unbounded.
	dispatched map[types.UID]struct{}
}

// Run blocks until ctx is cancelled. Idempotent across restarts:
// dispatched Jobs we haven't seen yet WILL re-fire after a server
// restart, but failedJobsHistoryLimit caps the window. For Tickero
// (refund-deadline-sweep at hourly cadence, failedJobsHistoryLimit=3)
// that means a restart in the middle of an outage at worst re-fires
// the last 3 failures. The signal is "your cron is failing" — a
// duplicate alert is much better than a missed one.
func (w *Watcher) Run(ctx context.Context) {
	if w == nil || w.Kube == nil {
		return
	}
	if w.Logger == nil {
		w.Logger = slog.Default()
	}
	if w.dispatched == nil {
		w.dispatched = map[types.UID]struct{}{}
	}
	if w.HTTP == nil {
		w.HTTP = newWebhookClient(w.Config.httpTimeout())
	}
	w.Logger.Info("cronwatch starting", "tick", w.Config.tick())
	t := time.NewTicker(w.Config.tick())
	defer t.Stop()
	w.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			w.Logger.Info("cronwatch stopping")
			return
		case <-t.C:
			w.tick(ctx)
		}
	}
}

func (w *Watcher) tick(ctx context.Context) {
	// List Jobs cluster-wide with the kuso.sislelabs.com/cron label.
	// CronJob-spawned Jobs inherit labels from the jobTemplate +
	// the kusocron chart's _helpers.tpl emits this on every Job.
	//
	// Bound the LIST with a per-call timeout — the kube REST client has
	// no global deadline, so a hung apiserver would otherwise block this
	// tick forever and silently stop all cron-failure alerting. nodewatch
	// and nodemetrics guard their LISTs the same way.
	listCtx, cancel := context.WithTimeout(ctx, w.Config.kubeTimeout())
	jobs, err := w.Kube.Clientset.BatchV1().Jobs("").List(listCtx, metav1.ListOptions{
		LabelSelector: "kuso.sislelabs.com/cron",
	})
	cancel()
	if err != nil {
		w.Logger.Warn("cronwatch list jobs", "err", err)
		return
	}
	// Fire each newly-failed Job's handler in its own goroutine so a
	// single slow/unreachable webhook (up to ~15s of retry backoff)
	// can't serialize detection of every other failed cron in this
	// tick. The UID is marked dispatched BEFORE the goroutine starts,
	// so a concurrent tick won't double-fire. We wait for all handlers
	// before returning so the prune below sees a stable state and the
	// goroutines stay bounded (never more than one tick's worth live).
	var wg sync.WaitGroup
	for i := range jobs.Items {
		job := &jobs.Items[i]
		if !isFailed(job) {
			continue
		}
		w.mu.Lock()
		if _, seen := w.dispatched[job.UID]; seen {
			w.mu.Unlock()
			continue
		}
		w.dispatched[job.UID] = struct{}{}
		w.mu.Unlock()
		wg.Add(1)
		go func(j *batchv1.Job) {
			defer wg.Done()
			w.handleFailure(ctx, j)
		}(job)
	}
	wg.Wait()
	// Prune dispatched UIDs that no longer correspond to a live Job.
	// failedJobsHistoryLimit bounds Jobs IN THE CLUSTER, not entries in
	// this map — without an explicit prune the map grows for the life of
	// the process as failed Jobs age out. We have the full current Job
	// list in hand, so drop any UID that isn't in it.
	live := make(map[types.UID]struct{}, len(jobs.Items))
	for i := range jobs.Items {
		live[jobs.Items[i].UID] = struct{}{}
	}
	w.mu.Lock()
	for uid := range w.dispatched {
		if _, ok := live[uid]; !ok {
			delete(w.dispatched, uid)
		}
	}
	w.mu.Unlock()
}

// isFailed checks Job.Status.Conditions for a terminal Failed=True.
// We deliberately don't fire on Jobs that are merely retrying — the
// backoffLimit on the cronjob template decides when retries stop.
func isFailed(job *batchv1.Job) bool {
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func (w *Watcher) handleFailure(ctx context.Context, job *batchv1.Job) {
	cronName := job.Labels["kuso.sislelabs.com/cron"]
	if cronName == "" {
		return
	}
	// Bound the CR read — same reasoning as the tick LIST: a hung
	// apiserver must not wedge the handler goroutine.
	getCtx, cancel := context.WithTimeout(ctx, w.Config.kubeTimeout())
	cron, err := w.Kube.GetKusoCron(getCtx, job.Namespace, cronName)
	cancel()
	if err != nil {
		w.Logger.Warn("cronwatch resolve cron", "err", err, "cron", cronName, "ns", job.Namespace)
		return
	}
	project := cron.Spec.Project
	service := cron.Spec.Service
	w.Logger.Info("cronwatch cron failed",
		"project", project, "service", service, "cron", cronName,
		"job", job.Name)

	// Always emit the notify event so the bell + global webhooks
	// pick it up. Per-cron onFailure webhook fires in addition.
	w.emitNotify(cron, job)

	if cron.Spec.OnFailure != nil && cron.Spec.OnFailure.WebhookURL != "" {
		if err := w.dispatchWebhook(ctx, cron, job); err != nil {
			w.Logger.Warn("cronwatch webhook", "err", err, "cron", cronName)
		}
	}
}

func (w *Watcher) emitNotify(cron *kube.KusoCron, job *batchv1.Job) {
	if w.Notify == nil {
		return
	}
	project := cron.Spec.Project
	service := cron.Spec.Service
	cronName := cron.Name
	startedAt, finishedAt := jobTimestamps(job)
	w.Notify.Emit(notify.Event{
		Type:      notify.EventCronFailed,
		Timestamp: time.Now().UTC(),
		Project:   project,
		Service:   service,
		Title:     fmt.Sprintf("Cron failed: %s", cronName),
		Description: fmt.Sprintf("Job %s exited with failure. Started %s, finished %s.",
			job.Name, startedAt, finishedAt),
		URL:      w.logsURL(project, service, cronName, job.Name),
		Severity: "warning",
		Fields: []notify.EventField{
			{Name: "cron", Value: cronName, Inline: true},
			{Name: "job", Value: job.Name, Inline: true},
		},
	})
}

// httpClient returns the configured webhook client, lazily defaulting
// one bounded by httpTimeout. Run() sets w.HTTP up front; this keeps a
// Watcher usable when tick/handleFailure are driven directly (tests, or
// any caller that skips Run).
func (w *Watcher) httpClient() *http.Client {
	if w.HTTP != nil {
		return w.HTTP
	}
	return newWebhookClient(w.Config.httpTimeout())
}

// newWebhookClient builds the outbound webhook client with the shared
// SSRF-safe transport. The onFailure webhook URL is user-supplied
// (anyone who can edit a cron sets it), so a bare http.Client would
// happily POST the failure payload — including the deep-link logsURL —
// at 169.254.169.254 (cloud metadata) or 10.0.0.0/8 (in-cluster
// apiserver / addon DBs). The httpx transport resolves the host,
// rejects reserved/private IPs, and re-dials the resolved IP so a DNS
// rebind between check and dial can't slip through — string validation
// of the stored URL alone is rebinding-racy. Mirrors the notify
// dispatcher's client construction.
func newWebhookClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: httpx.SSRFSafeTransport(),
	}
}

// validateWebhookURLFn is an overridable seam so the dispatch tests
// (which point at loopback httptest servers) can relax the shape check;
// the SSRFSafeTransport dial guard is exercised separately. Production
// always uses validateWebhookURL.
var validateWebhookURLFn = validateWebhookURL

// validateWebhookURL is the dispatch-time shape check for the stored
// onFailure webhook URL. It mirrors the handler-side validator
// (internal/http/handlers/notifications.go) but shares the httpx
// reserved-IP policy so an IP-literal in a private range is rejected
// consistently. This is a cheap allowlist; the SSRFSafeTransport dialer
// is what actually defeats DNS rebinding at dial time.
func validateWebhookURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("scheme must be http or https")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("missing host")
	}
	hostLower := strings.ToLower(host)
	if hostLower == "localhost" {
		return fmt.Errorf("localhost is not allowed")
	}
	for _, suf := range []string{".svc", ".svc.cluster.local", ".cluster.local", ".internal", ".local"} {
		if strings.HasSuffix(hostLower, suf) {
			return fmt.Errorf("cluster-internal hostnames (%s) are not allowed", suf)
		}
	}
	if ip := net.ParseIP(host); ip != nil && httpx.IsReservedIP(ip) {
		return fmt.Errorf("IP %s is in a reserved/private range", ip)
	}
	return nil
}

// Payload is the JSON body POSTed to the per-cron onFailure webhook.
// Stable wire shape — clients (Slack handlers, oncall scripts) may
// rely on field names.
type Payload struct {
	Project    string `json:"project"`
	Service    string `json:"service,omitempty"`
	Cron       string `json:"cron"`
	JobName    string `json:"jobName"`
	ExitCode   int32  `json:"exitCode,omitempty"`
	StartedAt  string `json:"startedAt,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`
	LogsURL    string `json:"logsURL,omitempty"`
}

func (w *Watcher) dispatchWebhook(ctx context.Context, cron *kube.KusoCron, job *batchv1.Job) error {
	// Cheap pre-flight on the stored URL. The SSRF-safe transport is the
	// load-bearing defence (it re-resolves + rejects at dial time, which
	// a DNS-rebinding attack can't beat), but rejecting an obviously bad
	// scheme/host/IP-literal here fails fast without a wasted dial and
	// keeps the check visible next to the call site.
	if err := validateWebhookURLFn(cron.Spec.OnFailure.WebhookURL); err != nil {
		return fmt.Errorf("reject webhook url: %w", err)
	}
	startedAt, finishedAt := jobTimestamps(job)
	p := Payload{
		Project:    cron.Spec.Project,
		Service:    cron.Spec.Service,
		Cron:       cron.Name,
		JobName:    job.Name,
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
		LogsURL:    w.logsURL(cron.Spec.Project, cron.Spec.Service, cron.Name, job.Name),
	}
	body, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	var sig string
	if cron.Spec.OnFailure.SecretRef != nil {
		s, sigErr := w.signBody(ctx, job.Namespace, cron.Spec.OnFailure.SecretRef, body)
		if sigErr != nil {
			return fmt.Errorf("sign body: %w", sigErr)
		}
		sig = s
	}
	// Retry: 1 attempt + 2 retries with linear backoff (1s, 4s).
	// Webhooks are best-effort; a hard fail just logs.
	//
	// A FRESH request is built per attempt: the body is a bytes.Reader
	// that is at EOF after the first Do, so reusing one *http.Request
	// would make every retry fail client-side with "ContentLength=N with
	// Body length 0" — i.e. the retry loop was previously dead code.
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * time.Second):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, cron.Spec.OnFailure.WebhookURL, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "kuso-cronwatch/1")
		if sig != "" {
			req.Header.Set("X-Kuso-Signature", "sha256="+sig)
		}
		resp, err := w.httpClient().Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		ok := resp.StatusCode >= 200 && resp.StatusCode < 300
		// Drain + close before the next attempt so the connection can be
		// reused and nothing leaks across loop iterations.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		resp.Body.Close()
		if ok {
			return nil
		}
		lastErr = fmt.Errorf("webhook returned %d", resp.StatusCode)
	}
	return lastErr
}

func (w *Watcher) signBody(ctx context.Context, ns string, ref *kube.KusoSecretKeyRef, body []byte) (string, error) {
	getCtx, cancel := context.WithTimeout(ctx, w.Config.kubeTimeout())
	defer cancel()
	sec, err := w.Kube.Clientset.CoreV1().Secrets(ns).Get(getCtx, ref.Name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get signing secret %s/%s: %w", ns, ref.Name, err)
	}
	key, ok := sec.Data[ref.Key]
	if !ok || len(key) == 0 {
		return "", fmt.Errorf("signing secret %s/%s missing key %q", ns, ref.Name, ref.Key)
	}
	h := hmac.New(sha256.New, key)
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (w *Watcher) logsURL(project, service, cron, jobName string) string {
	if w.BaseURL == "" {
		return ""
	}
	if service == "" {
		return fmt.Sprintf("%s/projects/%s/crons/%s/runs/%s", w.BaseURL, project, cron, jobName)
	}
	return fmt.Sprintf("%s/projects/%s/services/%s/crons/%s/runs/%s",
		w.BaseURL, project, service, cron, jobName)
}

func jobTimestamps(job *batchv1.Job) (started, finished string) {
	if job.Status.StartTime != nil {
		started = job.Status.StartTime.UTC().Format(time.RFC3339)
	}
	if job.Status.CompletionTime != nil {
		finished = job.Status.CompletionTime.UTC().Format(time.RFC3339)
	} else {
		// Failed Jobs don't set CompletionTime — use the last
		// transition time of the Failed condition.
		for _, c := range job.Status.Conditions {
			if c.Type == batchv1.JobFailed {
				finished = c.LastTransitionTime.UTC().Format(time.RFC3339)
				break
			}
		}
	}
	return
}
