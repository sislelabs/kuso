// Package cronwatch detects failed scheduled jobs (KusoCron-owned)
// and dispatches them via the per-cron onFailure webhook + the
// shared notify dispatcher.
//
// Loop:
//   - Every Tick (default 30s), list every Job in every namespace
//     labeled kuso.sislelabs.com/cron.
//   - For Jobs in terminal Failed state we haven't seen before,
//     resolve the parent KusoCron and dispatch.
//   - Idempotency: once a Job's failure is dispatched it is stamped
//     with the notifiedAnnotation, which survives server restarts,
//     leader changes and self-update rolls. An in-memory UID set
//     covers the window before the informer cache sees the stamp.
//     Failures older than staleFailureAge that were never stamped
//     (pre-upgrade history, or a stamp that failed to land) are
//     stamped silently instead of alerting.
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
	"strconv"
	"strings"
	"sync"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/httpx"
	"kuso/server/internal/kube"
	"kuso/server/internal/notify"
	"kuso/server/internal/serverstate"
)

// notifiedAnnotation marks a failed Job whose cron.failed alert has
// already gone out. Stored on the Job itself so the dedupe survives a
// restart; the Job's own GC (failedJobsHistoryLimit) cleans it up.
const notifiedAnnotation = "kuso.sislelabs.com/cron-failure-notified"

// staleFailureAge bounds how old an unstamped failure may be and still
// alert. A Job that failed during a restart or roll (minutes) still
// alerts once; history that predates this dedupe, or a Job whose stamp
// patch failed, doesn't replay days-old failures on every restart.
const staleFailureAge = time.Hour

// Config tunes the loop. Zero values fall back to defaults.
type Config struct {
	Tick time.Duration
	// HTTPTimeout caps the outbound webhook call. Defaults to 5s —
	// long enough for a Slack/Discord webhook to ack, short enough
	// that a misbehaving endpoint doesn't pile up.
	HTTPTimeout time.Duration
}

// DefaultTickInterval is the watcher's tick cadence when Config.Tick is
// unset. Exported so main.go can register cronwatch in the serverstate
// liveness registry at the cadence it beats. main.go constructs the
// Watcher with the zero-value Config, so this is the effective interval.
const DefaultTickInterval = 30 * time.Second

func (c Config) tick() time.Duration {
	if c.Tick <= 0 {
		return DefaultTickInterval
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
	// Logs is logship's archive, read for the card's log tail when the
	// failed Job's pods are already gone. Nil = live pods only.
	Logs LogArchive

	mu sync.Mutex
	// dispatched records Job UIDs we've already fired for, so a
	// failed Job that sticks around (failedJobsHistoryLimit > 0)
	// doesn't re-fire on every tick. Pruned each tick against the live
	// Job set (see tick) so it doesn't grow unbounded.
	dispatched map[types.UID]struct{}

	// now is overridable for tests; nil means time.Now.
	now func() time.Time
	// emit is a test seam for the notify fan-out; nil = Notify.Emit.
	emit func(notify.Event)
}

// LogArchive reads a pod's archived output after the pod is gone.
// Implemented by *db.LogDB. Lines come back oldest-first.
type LogArchive interface {
	PodLogTail(ctx context.Context, project, service, podPrefix string, since time.Time, n int) ([]string, error)
}

// Run blocks until ctx is cancelled. Dedupe across restarts relies on
// notifiedAnnotation (see package doc): a Job is stamped after its
// alert is dispatched, so a crash between dispatch and stamp can
// re-fire that one Job, but a normal restart never replays history.
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
			serverstate.LoopHeartbeat(serverstate.LoopCronWatch)
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
	// Prefer the shared Job informer: this is a CLUSTER-WIDE list on a
	// 30s tick, so on a busy cluster it was one of the largest single
	// sources of apiserver load in the control plane. The informer
	// already watches Jobs for the build/runs pollers; reusing it here
	// costs nothing. Falls back to a live LIST when the cache is cold
	// (fresh boot) or absent (tests).
	var jobList []*batchv1.Job
	sel, selErr := labels.Parse("kuso.sislelabs.com/cron")
	if selErr == nil && w.Kube.Cache != nil {
		if cached, ok := w.Kube.Cache.ListJobs("", sel); ok {
			jobList = cached
		}
	}
	if jobList == nil {
		listCtx, cancel := context.WithTimeout(ctx, w.Config.kubeTimeout())
		jobs, err := w.Kube.Clientset.BatchV1().Jobs("").List(listCtx, metav1.ListOptions{
			LabelSelector: "kuso.sislelabs.com/cron",
		})
		cancel()
		if err != nil {
			w.Logger.Warn("cronwatch list jobs", "err", err)
			return
		}
		jobList = make([]*batchv1.Job, 0, len(jobs.Items))
		for i := range jobs.Items {
			jobList = append(jobList, &jobs.Items[i])
		}
	}
	// Fire each newly-failed Job's handler in its own goroutine so a
	// single slow/unreachable webhook (up to ~15s of retry backoff)
	// can't serialize detection of every other failed cron in this
	// tick. The UID is marked dispatched BEFORE the goroutine starts,
	// so a concurrent tick won't double-fire. We wait for all handlers
	// before returning so the prune below sees a stable state and the
	// goroutines stay bounded (never more than one tick's worth live).
	var wg sync.WaitGroup
	for i := range jobList {
		job := jobList[i]
		if !isFailed(job) || job.Annotations[notifiedAnnotation] != "" {
			continue
		}
		w.mu.Lock()
		if _, seen := w.dispatched[job.UID]; seen {
			w.mu.Unlock()
			continue
		}
		w.dispatched[job.UID] = struct{}{}
		w.mu.Unlock()
		if ft := failureTime(job); !ft.IsZero() && w.clock().Sub(ft) > staleFailureAge {
			w.Logger.Info("cronwatch skipping stale unnotified failure",
				"job", job.Name, "ns", job.Namespace, "failedAt", ft)
			w.markNotified(ctx, job)
			continue
		}
		wg.Add(1)
		go func(j *batchv1.Job) {
			defer wg.Done()
			if !w.handleFailure(ctx, j) {
				// Transient cron lookup failure: leave the Job unmarked
				// so the next tick retries; staleFailureAge bounds it.
				w.mu.Lock()
				delete(w.dispatched, j.UID)
				w.mu.Unlock()
				return
			}
			w.markNotified(ctx, j)
		}(job)
	}
	wg.Wait()
	// Prune dispatched UIDs that no longer correspond to a live Job.
	// failedJobsHistoryLimit bounds Jobs IN THE CLUSTER, not entries in
	// this map — without an explicit prune the map grows for the life of
	// the process as failed Jobs age out. We have the full current Job
	// list in hand, so drop any UID that isn't in it.
	live := make(map[types.UID]struct{}, len(jobList))
	for i := range jobList {
		live[jobList[i].UID] = struct{}{}
	}
	w.mu.Lock()
	for uid := range w.dispatched {
		if _, ok := live[uid]; !ok {
			delete(w.dispatched, uid)
		}
	}
	w.mu.Unlock()
}

func (w *Watcher) clock() time.Time {
	if w.now != nil {
		return w.now()
	}
	return time.Now()
}

// markNotified stamps notifiedAnnotation on the Job so a later process
// (restart, new leader) skips it. A failed patch is logged, not fatal:
// the in-memory set still dedupes for this process, and staleFailureAge
// stops a restart from replaying it once it's old.
func (w *Watcher) markNotified(ctx context.Context, job *batchv1.Job) {
	patch, err := json.Marshal(map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]string{
				notifiedAnnotation: w.clock().UTC().Format(time.RFC3339),
			},
		},
	})
	if err != nil {
		return
	}
	pctx, cancel := context.WithTimeout(ctx, w.Config.kubeTimeout())
	defer cancel()
	if _, err := w.Kube.Clientset.BatchV1().Jobs(job.Namespace).Patch(
		pctx, job.Name, types.MergePatchType, patch, metav1.PatchOptions{},
	); err != nil {
		w.Logger.Warn("cronwatch stamp notified", "err", err, "job", job.Name, "ns", job.Namespace)
	}
}

// failureTime is when the Job hit its terminal Failed condition, or
// zero if unknown (treated as fresh so it still alerts).
func failureTime(job *batchv1.Job) time.Time {
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			return c.LastTransitionTime.Time
		}
	}
	return time.Time{}
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

// handleFailure alerts on one failed Job. It returns false only when the
// alert could not be sent for a reason worth retrying.
func (w *Watcher) handleFailure(ctx context.Context, job *batchv1.Job) bool {
	cronName := job.Labels["kuso.sislelabs.com/cron"]
	if cronName == "" {
		return true
	}
	// Bound the CR read — same reasoning as the tick LIST: a hung
	// apiserver must not wedge the handler goroutine.
	getCtx, cancel := context.WithTimeout(ctx, w.Config.kubeTimeout())
	cron, err := w.Kube.GetKusoCron(getCtx, job.Namespace, cronName)
	cancel()
	if err != nil {
		w.Logger.Warn("cronwatch resolve cron", "err", err, "cron", cronName, "ns", job.Namespace)
		return apierrors.IsNotFound(err)
	}
	project := cron.Spec.Project
	service := cron.Spec.Service
	w.Logger.Info("cronwatch cron failed",
		"project", project, "service", service, "cron", cronName,
		"job", job.Name)

	// Always emit the notify event so the bell + global webhooks
	// pick it up. Per-cron onFailure webhook fires in addition.
	w.emitNotify(ctx, cron, job)

	if cron.Spec.OnFailure != nil && cron.Spec.OnFailure.WebhookURL != "" {
		if err := w.dispatchWebhook(ctx, cron, job); err != nil {
			w.Logger.Warn("cronwatch webhook", "err", err, "cron", cronName)
		}
	}
	return true
}

func (w *Watcher) emitNotify(ctx context.Context, cron *kube.KusoCron, job *batchv1.Job) {
	emit := w.emit
	if emit == nil {
		if w.Notify == nil {
			return
		}
		emit = w.Notify.Emit
	}
	emit(cronFailedEvent(cron, job, w.failedPod(ctx, job)))
}

// podFailure is what the failed Job's newest pod says about the run.
// Zero value = no pod found (GC'd, or the list failed).
type podFailure struct {
	exitCode *int32
	logTail  string
}

// cronFailedEvent renders the cron.failed card.
func cronFailedEvent(cron *kube.KusoCron, job *batchv1.Job, pf podFailure) notify.Event {
	project := cron.Spec.Project
	short := strings.TrimPrefix(cron.Spec.Service, project+"-")

	desc := "Job failed"
	started, finished := jobTimes(job)
	if !finished.IsZero() {
		desc = "Failed " + notify.TimeToken(finished)
		if !started.IsZero() && finished.After(started) {
			desc += " after " + shortDuration(finished.Sub(started))
		}
	}

	var fields []notify.EventField
	if short != "" {
		fields = append(fields, notify.EventField{Name: "Service", Value: short, Inline: true})
	}
	if cron.Spec.Schedule != "" {
		fields = append(fields, notify.EventField{Name: "Schedule", Value: "`" + cron.Spec.Schedule + "`", Inline: true})
	}
	body := "Job " + job.Name + " failed"
	if pf.exitCode != nil {
		code := strconv.Itoa(int(*pf.exitCode))
		fields = append(fields, notify.EventField{Name: "Exit code", Value: code, Inline: true})
		body += " (exit code " + code + ")"
	}
	fields = append(fields, notify.EventField{Name: "Job", Value: "`" + job.Name + "`", Inline: true})

	var links []notify.EventLink
	if short != "" {
		links = append(links,
			notify.EventLink{Label: "Crons", URL: cronURL(project, cron.Spec.Service)},
			notify.EventLink{Label: "Logs", URL: notify.ServiceLink(project, short, "logs", "")},
		)
	} else if u := cronURL(project, ""); u != "" {
		links = append(links, notify.EventLink{Label: "Project", URL: u})
	}

	return notify.Event{
		Type:      notify.EventCronFailed,
		Timestamp: time.Now().UTC(),
		Project:   project,
		// Short slug, like every other event: the bell feed + incidents
		// key on it and the web's ?service= expects it.
		Service:     short,
		Title:       "✗ Cron failed · " + project + " / " + cron.Name,
		Description: desc,
		Body:        body,
		LogTail:     pf.logTail,
		URL:         cronURL(project, cron.Spec.Service),
		Severity:    "warn",
		Fields:      fields,
		Links:       links,
	}
}

// logTailLines is how much of the failed pod's output rides on the card.
const logTailLines = 5

// failedPod finds the Job's newest pod and reads its exit code and last
// few log lines, falling back to the log archive for the tail when the
// pod is gone. Best-effort and time-bounded: any failure just leaves the
// card without them.
func (w *Watcher) failedPod(ctx context.Context, job *batchv1.Job) podFailure {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	pf := w.livePodFailure(ctx, job)
	if pf.logTail == "" {
		pf.logTail = w.archivedTail(ctx, job)
	}
	return pf
}

// archivedTail reads the Job's last lines from logship's archive. A
// restartPolicy=OnFailure Job that exhausts its backoffLimit has its pod
// deleted by the Job controller before Failed is set, so by the time we
// see the failure there's usually no pod left to read.
func (w *Watcher) archivedTail(ctx context.Context, job *batchv1.Job) string {
	if w.Logs == nil {
		return ""
	}
	// Pod names are "<job>-<5 char suffix>"; logship stores the pod's
	// project/service labels, which the kusocron chart copies from the Job.
	lines, err := w.Logs.PodLogTail(ctx,
		job.Labels["kuso.sislelabs.com/project"], job.Labels["kuso.sislelabs.com/service"],
		job.Name+"-", job.CreationTimestamp.Time, logTailLines)
	if err != nil {
		if w.Logger != nil {
			w.Logger.Warn("cronwatch archived log tail", "err", err, "job", job.Name)
		}
		return ""
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func (w *Watcher) livePodFailure(ctx context.Context, job *batchv1.Job) podFailure {
	var pf podFailure
	if w.Kube == nil || w.Kube.Clientset == nil {
		return pf
	}
	pods, err := w.Kube.Clientset.CoreV1().Pods(job.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "job-name=" + job.Name,
	})
	if err != nil || len(pods.Items) == 0 {
		return pf
	}
	pod := &pods.Items[0]
	for i := range pods.Items {
		if pods.Items[i].CreationTimestamp.After(pod.CreationTimestamp.Time) {
			pod = &pods.Items[i]
		}
	}
	container := ""
	for _, cs := range pod.Status.ContainerStatuses {
		term := cs.State.Terminated
		if term == nil {
			term = cs.LastTerminationState.Terminated
		}
		if term != nil && (pf.exitCode == nil || term.ExitCode != 0) {
			code := term.ExitCode
			pf.exitCode = &code
			container = cs.Name
		}
	}
	tail := int64(logTailLines)
	stream, err := w.Kube.Clientset.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
		Container: container,
		TailLines: &tail,
	}).Stream(ctx)
	if err != nil {
		return pf
	}
	defer stream.Close()
	data, _ := io.ReadAll(io.LimitReader(stream, 8<<10))
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) > logTailLines {
		lines = lines[len(lines)-logTailLines:]
	}
	pf.logTail = strings.TrimSpace(strings.Join(lines, "\n"))
	return pf
}

// shortDuration formats a run length as "41s", "3m 12s", "2h 5m".
func shortDuration(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + "s"
	case d < time.Hour:
		m, s := int(d/time.Minute), int((d%time.Minute)/time.Second)
		if s == 0 {
			return strconv.Itoa(m) + "m"
		}
		return fmt.Sprintf("%dm %ds", m, s)
	default:
		h, m := int(d/time.Hour), int((d%time.Hour)/time.Minute)
		if m == 0 {
			return strconv.Itoa(h) + "h"
		}
		return fmt.Sprintf("%dh %dm", h, m)
	}
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
// of the stored URL alone is rebinding-racy. Redirects are refused
// outright (a 302 hop gets its own DNS resolution and webhook POST
// delivery has no legitimate redirect use). Mirrors the notify
// dispatcher's client construction.
func newWebhookClient(timeout time.Duration) *http.Client {
	return httpx.SSRFSafeNoRedirectClient(timeout)
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
		LogsURL:    w.logsURL(cron.Spec.Project, cron.Spec.Service),
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

// cronURL is the in-app path for a failed cron. The web is a static
// export with only /projects/[project] routes; the service overlay is
// opened by ?service=<short>&tab=<tab>. Service-kind crons land on the
// overlay's Crons tab; project-scoped crons (no service) have no deep
// link target beyond the project canvas. cron.Spec.Service is the FQN
// (<project>-<svc>), so strip it to the short slug the web keys on.
func cronURL(project, service string) string {
	if project == "" {
		return ""
	}
	p := url.PathEscape(project)
	short := strings.TrimPrefix(service, project+"-")
	if short == "" {
		return "/projects/" + p
	}
	return "/projects/" + p + "?service=" + url.QueryEscape(short) + "&tab=crons"
}

// logsURL is the absolute deep link for the onFailure webhook payload,
// which goes to arbitrary receivers that can't resolve relative paths.
// Empty when BaseURL is unset.
func (w *Watcher) logsURL(project, service string) string {
	rel := cronURL(project, service)
	if w.BaseURL == "" || rel == "" {
		return ""
	}
	return strings.TrimRight(w.BaseURL, "/") + rel
}

func jobTimestamps(job *batchv1.Job) (started, finished string) {
	s, f := jobTimes(job)
	if !s.IsZero() {
		started = s.UTC().Format(time.RFC3339)
	}
	if !f.IsZero() {
		finished = f.UTC().Format(time.RFC3339)
	}
	return
}

func jobTimes(job *batchv1.Job) (started, finished time.Time) {
	if job.Status.StartTime != nil {
		started = job.Status.StartTime.Time
	}
	if job.Status.CompletionTime != nil {
		finished = job.Status.CompletionTime.Time
	} else {
		// Failed Jobs don't set CompletionTime — use the last
		// transition time of the Failed condition.
		for _, c := range job.Status.Conditions {
			if c.Type == batchv1.JobFailed {
				finished = c.LastTransitionTime.Time
				break
			}
		}
	}
	return
}
