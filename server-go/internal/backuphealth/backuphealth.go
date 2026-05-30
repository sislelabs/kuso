// Package backuphealth inspects the control-plane Postgres backup
// CronJob (deploy/postgres-backup.yaml) and reports whether the kuso DB
// is actually being backed up off-cluster.
//
// The backup is opt-in: without the operator-supplied
// kuso-postgres-backup Secret the CronJob suspends itself silently, and
// a failing backup is invisible until restore time — when it's too
// late. This package turns that blind spot into (a) a status the
// settings UI renders as a banner and (b) a Watcher that fires a
// one-shot notify event on the healthy↔unhealthy edge so an operator
// who never opens the page still finds out.
package backuphealth

import (
	"context"
	"log/slog"
	"sort"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
	"kuso/server/internal/notify"
)

const (
	secretName  = "kuso-postgres-backup"
	cronJobName = "kuso-postgres-backup"
	jobLabel    = "app.kubernetes.io/name=kuso-postgres-backup"
	// StaleAfter: the CronJob runs hourly; if the newest success is
	// older than this we flag stale (tolerates one transient
	// failure+retry before crying wolf).
	StaleAfter = 3 * time.Hour
)

// Status is the verdict the UI banner + watcher consume.
type Status struct {
	Configured     bool   `json:"configured"`
	CronJobPresent bool   `json:"cronJobPresent"`
	Suspended      bool   `json:"suspended"`
	Schedule       string `json:"schedule,omitempty"`
	LastSuccessAt  string `json:"lastSuccessAt,omitempty"`
	LastFailureAt  string `json:"lastFailureAt,omitempty"`
	Stale          bool   `json:"stale"`
	Healthy        bool   `json:"healthy"`
	Detail         string `json:"detail"`
}

// Compute reads the Secret + CronJob + recent Jobs and derives the
// verdict. Three cheap kube reads; safe to call on a UI request or a
// watcher tick.
func Compute(ctx context.Context, kc *kube.Client, namespace string) Status {
	var s Status
	if kc == nil {
		s.Detail = detail(s)
		return s
	}

	if _, err := kc.Clientset.CoreV1().Secrets(namespace).
		Get(ctx, secretName, metav1.GetOptions{}); err == nil {
		s.Configured = true
	}

	if cj, err := kc.Clientset.BatchV1().CronJobs(namespace).
		Get(ctx, cronJobName, metav1.GetOptions{}); err == nil {
		s.CronJobPresent = true
		s.Schedule = cj.Spec.Schedule
		if cj.Spec.Suspend != nil && *cj.Spec.Suspend {
			s.Suspended = true
		}
	}

	if jobs, err := kc.Clientset.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: jobLabel,
	}); err == nil {
		success, failure := newestTerminalTimes(jobs.Items)
		if !success.IsZero() {
			s.LastSuccessAt = success.UTC().Format(time.RFC3339)
		}
		if !failure.IsZero() {
			s.LastFailureAt = failure.UTC().Format(time.RFC3339)
		}
		s.Stale = success.IsZero() || time.Since(success) > StaleAfter
	} else {
		s.Stale = true // fail-safe: can't read → don't claim healthy
	}

	s.Healthy = s.Configured && s.CronJobPresent && !s.Suspended && !s.Stale
	s.Detail = detail(s)
	return s
}

func newestTerminalTimes(jobs []batchv1.Job) (success, failure time.Time) {
	for i := range jobs {
		j := &jobs[i]
		t := terminalTime(j)
		if t.IsZero() {
			continue
		}
		switch {
		case j.Status.Succeeded > 0:
			if t.After(success) {
				success = t
			}
		case j.Status.Failed > 0:
			if t.After(failure) {
				failure = t
			}
		}
	}
	return success, failure
}

func terminalTime(j *batchv1.Job) time.Time {
	if j.Status.CompletionTime != nil {
		return j.Status.CompletionTime.Time
	}
	conds := append([]batchv1.JobCondition(nil), j.Status.Conditions...)
	sort.Slice(conds, func(a, b int) bool {
		return conds[a].LastTransitionTime.After(conds[b].LastTransitionTime.Time)
	})
	for _, c := range conds {
		if c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed {
			return c.LastTransitionTime.Time
		}
	}
	if j.Status.StartTime != nil {
		return j.Status.StartTime.Time
	}
	return time.Time{}
}

// Watcher periodically checks backup health and fires a one-shot notify
// event on the healthy↔unhealthy edge — so an operator who never opens
// the settings page still learns their control-plane DB stopped being
// backed up. Edge-triggered (not per-tick) so it doesn't spam. Leader-
// gated by the caller (lives in the kube-singletons block).
type Watcher struct {
	Kube      *kube.Client
	Notify    *notify.Dispatcher
	Namespace string
	Logger    *slog.Logger
	// Interval between checks. Zero → 15m (backup is hourly; 15m is
	// responsive enough without hammering the apiserver).
	Interval time.Duration

	// unhealthy tracks the last-emitted state so we only fire on a flip.
	// nil = not yet evaluated (first tick establishes the baseline
	// without alerting on a cold start that's already unhealthy — we DO
	// want that first alert, so we treat nil as "previously healthy").
	lastUnhealthy bool
	evaluated     bool
}

func (w *Watcher) Run(ctx context.Context) {
	if w.Logger == nil {
		w.Logger = slog.Default()
	}
	interval := w.Interval
	if interval <= 0 {
		interval = 15 * time.Minute
	}
	// Initial delay so a fresh boot doesn't alert before the first
	// backup CronJob has had a chance to run.
	t := time.NewTimer(2 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		w.tick(ctx)
		t.Reset(interval)
	}
}

func (w *Watcher) tick(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	s := Compute(cctx, w.Kube, w.Namespace)
	gc := RegistryGC(cctx, w.Kube, w.Namespace)
	cancel()

	// Either subsystem being unhealthy trips the alert; the detail
	// names whichever is wrong (backup is the more severe, so it wins
	// the message when both are bad).
	unhealthy := !s.Healthy || !gc.Healthy
	detailMsg := s.Detail
	if s.Healthy && !gc.Healthy {
		detailMsg = gc.Detail
	}
	// Only emit on a state change (or the first observation of an
	// unhealthy state). evaluated guards the very first tick so we don't
	// double-fire.
	if w.evaluated && unhealthy == w.lastUnhealthy {
		w.evaluated, w.lastUnhealthy = true, unhealthy
		return
	}
	prevUnhealthy := w.lastUnhealthy
	w.evaluated, w.lastUnhealthy = true, unhealthy

	if w.Notify == nil {
		return
	}
	switch {
	case unhealthy:
		title := "Control-plane backup unhealthy"
		if s.Healthy && !gc.Healthy {
			title = "Registry garbage-collection unhealthy"
		}
		w.Notify.Emit(notify.Event{
			Type:        notify.EventBackupFailed,
			Timestamp:   time.Now().UTC(),
			Title:       title,
			Body:        detailMsg,
			Description: detailMsg,
			Severity:    "error",
		})
		w.Logger.Warn("backup/registry health: unhealthy", "detail", detailMsg)
	case prevUnhealthy:
		// Recovered (both healthy again).
		w.Notify.Emit(notify.Event{
			Type:      notify.EventBackupOK,
			Timestamp: time.Now().UTC(),
			Title:     "Backup / registry maintenance recovered",
			Body:      "Control-plane backups and registry GC are healthy again.",
			Severity:  "info",
		})
		w.Logger.Info("backup/registry health: recovered")
	}
}

// RegistryGCStatus reports the health of the weekly registry garbage-
// collection CronJob (deploy/registry.yaml). When the GC stops
// succeeding, the in-cluster registry PVC grows unbounded and builds
// eventually fail with an opaque "no space left on device" — this turns
// that into an early signal. Mirrors the backup status shape.
type RegistryGCStatus struct {
	CronJobPresent bool   `json:"cronJobPresent"`
	Suspended      bool   `json:"suspended"`
	Schedule       string `json:"schedule,omitempty"`
	LastSuccessAt  string `json:"lastSuccessAt,omitempty"`
	LastFailureAt  string `json:"lastFailureAt,omitempty"`
	// Stale: no success within ~9 days (the job is weekly; 9d tolerates
	// one missed run + slack before flagging).
	Stale   bool   `json:"stale"`
	Healthy bool   `json:"healthy"`
	Detail  string `json:"detail"`
}

const (
	registryGCCronJobName = "kuso-registry-gc"
	registryGCJobLabel    = "app.kubernetes.io/name=kuso-registry-gc"
	// registryGCStaleAfter: the GC is weekly, so tolerate one missed
	// run (14d) plus a couple days of slack before flagging — a single
	// skipped Sunday isn't an emergency, two in a row is.
	registryGCStaleAfter = 16 * 24 * time.Hour
)

// RegistryGC computes the registry-GC verdict from the GC CronJob + its
// recent Jobs. Two cheap kube reads.
func RegistryGC(ctx context.Context, kc *kube.Client, namespace string) RegistryGCStatus {
	var s RegistryGCStatus
	if kc == nil {
		s.Detail = registryGCDetail(s)
		return s
	}
	if cj, err := kc.Clientset.BatchV1().CronJobs(namespace).
		Get(ctx, registryGCCronJobName, metav1.GetOptions{}); err == nil {
		s.CronJobPresent = true
		s.Schedule = cj.Spec.Schedule
		if cj.Spec.Suspend != nil && *cj.Spec.Suspend {
			s.Suspended = true
		}
	}
	if jobs, err := kc.Clientset.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: registryGCJobLabel,
	}); err == nil {
		success, failure := newestTerminalTimes(jobs.Items)
		if !success.IsZero() {
			s.LastSuccessAt = success.UTC().Format(time.RFC3339)
		}
		if !failure.IsZero() {
			s.LastFailureAt = failure.UTC().Format(time.RFC3339)
		}
		// A GC that has never run yet (fresh install, first Sunday not
		// reached) is NOT stale — only flag once it's had a chance and
		// then lapsed. So: stale iff there's a success that's now old,
		// OR there's a failure but no success.
		switch {
		case !success.IsZero():
			s.Stale = time.Since(success) > registryGCStaleAfter
		case !failure.IsZero():
			s.Stale = true // failing with no success ever
		default:
			s.Stale = false // never run yet — warming up
		}
	}
	s.Healthy = s.CronJobPresent && !s.Suspended && !s.Stale
	s.Detail = registryGCDetail(s)
	return s
}

func registryGCDetail(s RegistryGCStatus) string {
	switch {
	case !s.CronJobPresent:
		return "Registry garbage-collection CronJob not found — the build-image registry will grow unbounded and eventually fill its disk."
	case s.Suspended:
		return "Registry garbage-collection is suspended — reclaimed image space will stop being freed."
	case s.Stale:
		return "Registry garbage-collection hasn't succeeded recently — the registry PVC may be filling. Check the kuso-registry-gc CronJob."
	default:
		return "Registry garbage-collection healthy."
	}
}

func detail(s Status) string {
	switch {
	case !s.CronJobPresent:
		return "Control-plane backup CronJob not found — the kuso DB is not being backed up off-cluster."
	case !s.Configured:
		return "Control-plane backups are not configured. Create the kuso-postgres-backup Secret with S3 credentials so the kuso DB is backed up off-cluster — without it a node/PVC loss orphans every project."
	case s.Suspended:
		return "Control-plane backup CronJob is suspended — no backups are being taken."
	case s.LastSuccessAt == "":
		return "Control-plane backups are configured but none have succeeded yet."
	case s.Stale:
		return "Control-plane backups are configured but the last successful backup is stale — backups may have silently stopped. Check the kuso-postgres-backup CronJob."
	default:
		return "Control-plane backups healthy."
	}
}
