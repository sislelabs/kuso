package vulnscan

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"kuso/server/internal/builds"
	"kuso/server/internal/db"
	"kuso/server/internal/registryauth"
)

const (
	workerInterval = 5 * time.Second
	// jobAppearGrace: a running scan whose Job can't be found is only
	// failed after this, so an informer/apiserver lag right after
	// creation isn't mistaken for a lost Job.
	jobAppearGrace = 2 * time.Minute
	// scanMaxRuntime backstops the Job's activeDeadlineSeconds (pod
	// pending on an unschedulable PVC never starts that clock).
	scanMaxRuntime = 12 * time.Minute
	// pendingGrace: a scan pod that can't be scheduled (or whose cache
	// volume never binds) for this long fails now rather than holding
	// the queue — and gated builds — until scanMaxRuntime.
	pendingGrace = 2 * time.Minute
	// maxLogBytes bounds the pod-log read; the framed report is a few
	// hundred KB even for large images.
	maxLogBytes = 32 << 20
)

// WorkerStore is the queue persistence the worker drives.
type WorkerStore interface {
	ListRunningVulnScans(ctx context.Context) ([]db.VulnScan, error)
	ClaimNextVulnScan(ctx context.Context) (*db.VulnScan, error)
	SetVulnScanJob(ctx context.Context, id int64, jobName string) error
	CompleteVulnScan(ctx context.Context, id int64, res db.VulnScanResult) error
	FailVulnScan(ctx context.Context, id int64, msg string) error
}

// Worker runs queued scans one at a time as Jobs in Namespace. All of
// its state is in the VulnScan rows, so a new leader picks up a scan the
// old one started.
type Worker struct {
	Kube      kubernetes.Interface
	Namespace string
	Store     WorkerStore
	Logger    *slog.Logger
	// OnFailed is told about every scan that ends failed (notification).
	OnFailed func(ctx context.Context, scan db.VulnScan, msg string)
	// Logs reads the scan pod's output. Nil = the pods/log API.
	Logs func(ctx context.Context, ns, jobName string) (string, error)
	Now  func() time.Time
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *Worker) log() *slog.Logger {
	if w.Logger != nil {
		return w.Logger
	}
	return slog.Default()
}

// Run steps the worker until ctx ends.
func (w *Worker) Run(ctx context.Context) {
	t := time.NewTicker(workerInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := w.Step(ctx); err != nil && ctx.Err() == nil {
				w.log().Warn("vulnscan worker step", "err", err)
			}
		}
	}
}

// Step advances the running scan, or starts the next queued one.
func (w *Worker) Step(ctx context.Context) error {
	running, err := w.Store.ListRunningVulnScans(ctx)
	if err != nil {
		return err
	}
	if len(running) > 0 {
		for _, s := range running {
			w.advance(ctx, s)
		}
		return nil
	}
	next, err := w.Store.ClaimNextVulnScan(ctx)
	if err != nil || next == nil {
		return err
	}
	w.start(ctx, *next)
	return nil
}

func (w *Worker) fail(ctx context.Context, s db.VulnScan, msg string) {
	if err := w.Store.FailVulnScan(ctx, s.ID, msg); err != nil {
		w.log().Warn("vulnscan: record failure", "scan", s.ID, "err", err)
		return
	}
	w.cleanupAuth(ctx, s.ID)
	w.log().Info("vulnscan: scan failed", "scan", s.ID, "image", s.ImageRef, "reason", s.Reason, "err", msg)
	if w.OnFailed != nil {
		w.OnFailed(ctx, s, msg)
	}
}

// start creates the scan's Job (and its credentials Secret).
func (w *Worker) start(ctx context.Context, s db.VulnScan) {
	if err := w.ensureCachePVC(ctx); err != nil {
		w.fail(ctx, s, "create trivy cache volume: "+err.Error())
		return
	}
	host := ImageHost(s.ImageRef)
	pullSecret := s.PullSecret
	if pullSecret == "" && host == builds.RegistryHost {
		// The kuso-registry credential, once minted, carries a
		// .dockerconfigjson for the registry host; without it the scan
		// stays anonymous exactly as before.
		if _, gerr := w.Kube.CoreV1().Secrets(w.Namespace).Get(ctx, registryauth.SecretName, metav1.GetOptions{}); gerr == nil {
			pullSecret = w.Namespace + "/" + registryauth.SecretName
		}
	}
	auth, err := w.credentials(ctx, pullSecret, host)
	if err != nil {
		// Anonymous is still worth a try: public images need nothing.
		w.log().Warn("vulnscan: pull credentials unavailable, scanning anonymously", "scan", s.ID, "err", err)
	}
	spec := jobSpec{
		ScanID:    s.ID,
		Namespace: w.Namespace,
		ImageRef:  s.ImageRef,
		Insecure:  host == builds.RegistryHost,
		WithAuth:  auth != nil,
	}
	job, err := w.Kube.BatchV1().Jobs(w.Namespace).Create(ctx, buildJob(spec), metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		job, err = w.Kube.BatchV1().Jobs(w.Namespace).Get(ctx, JobName(s.ID), metav1.GetOptions{})
	}
	if err != nil {
		w.fail(ctx, s, "create scan job: "+err.Error())
		return
	}
	if auth != nil {
		_, err := w.Kube.CoreV1().Secrets(w.Namespace).Create(ctx, buildAuthSecret(job, s.ID, auth), metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			w.deleteJob(ctx, job.Name)
			w.fail(ctx, s, "create scan credentials: "+err.Error())
			return
		}
	}
	if err := w.Store.SetVulnScanJob(ctx, s.ID, job.Name); err != nil {
		w.log().Warn("vulnscan: record job name", "scan", s.ID, "err", err)
	}
	w.log().Info("vulnscan: scan started", "scan", s.ID, "image", s.ImageRef, "job", job.Name, "reason", s.Reason)
}

// advance checks a running scan's Job and finishes the scan when the
// Job is done.
func (w *Worker) advance(ctx context.Context, s db.VulnScan) {
	if s.JobName == "" {
		w.start(ctx, s)
		return
	}
	job, err := w.Kube.BatchV1().Jobs(w.Namespace).Get(ctx, s.JobName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if w.now().Sub(s.StartedAt) > jobAppearGrace {
			w.fail(ctx, s, "scan job disappeared before it finished")
		}
		return
	}
	if err != nil {
		// A persistent error (RBAC, apiserver down) must not hold the
		// one-at-a-time queue forever.
		if w.now().Sub(s.StartedAt) > scanMaxRuntime {
			w.fail(ctx, s, "scan job unreadable: "+err.Error())
			return
		}
		w.log().Warn("vulnscan: get scan job", "scan", s.ID, "err", err)
		return
	}
	switch jobOutcome(job) {
	case batchv1.JobComplete:
		w.finish(ctx, s)
	case batchv1.JobFailed:
		msg := "scan job failed"
		if c := jobCondition(job, batchv1.JobFailed); c != nil && c.Reason == "DeadlineExceeded" {
			msg = "scan timed out"
		}
		if tail := w.errorTail(ctx, s.JobName); tail != "" {
			msg += ": " + tail
		}
		w.fail(ctx, s, msg)
	default:
		if msg := w.stuckPending(ctx, s); msg != "" {
			w.deleteJob(ctx, s.JobName)
			w.fail(ctx, s, msg)
			return
		}
		if w.now().Sub(s.StartedAt) > scanMaxRuntime {
			w.deleteJob(ctx, s.JobName)
			w.fail(ctx, s, "scan timed out (job never finished)")
		}
	}
}

// stuckPending explains why a scan pod has sat Pending past
// pendingGrace (unschedulable, or the cache PVC unbound), else "".
func (w *Worker) stuckPending(ctx context.Context, s db.VulnScan) string {
	now := w.now()
	if now.Sub(s.StartedAt) <= pendingGrace {
		return ""
	}
	pods, err := w.Kube.CoreV1().Pods(w.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "job-name=" + s.JobName})
	if err != nil {
		return ""
	}
	for _, p := range pods.Items {
		if p.Status.Phase != corev1.PodPending {
			continue
		}
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Reason == corev1.PodReasonUnschedulable &&
				now.Sub(c.LastTransitionTime.Time) > pendingGrace {
				return "scan pod unschedulable: " + clip(c.Message, 500)
			}
		}
		pvc, err := w.Kube.CoreV1().PersistentVolumeClaims(w.Namespace).Get(ctx, CachePVC, metav1.GetOptions{})
		if err == nil && pvc.Status.Phase == corev1.ClaimPending {
			return fmt.Sprintf("trivy cache volume %s/%s is still Pending (no storage class or provisioner?)", w.Namespace, CachePVC)
		}
	}
	return ""
}

func (w *Worker) finish(ctx context.Context, s db.VulnScan) {
	logs, err := w.readLogs(ctx, s.JobName)
	if err != nil {
		w.fail(ctx, s, "read scan output: "+err.Error())
		return
	}
	out, err := decodeOutput(logs)
	if err != nil {
		w.fail(ctx, s, err.Error())
		return
	}
	res, err := parseOutput(out)
	if err != nil {
		w.fail(ctx, s, err.Error())
		return
	}
	if err := w.Store.CompleteVulnScan(ctx, s.ID, res); err != nil {
		w.log().Warn("vulnscan: store results", "scan", s.ID, "err", err)
		return
	}
	w.cleanupAuth(ctx, s.ID)
	w.log().Info("vulnscan: scan succeeded", "scan", s.ID, "image", s.ImageRef, "findings", len(res.Findings))
}

func jobCondition(job *batchv1.Job, t batchv1.JobConditionType) *batchv1.JobCondition {
	for i := range job.Status.Conditions {
		c := &job.Status.Conditions[i]
		if c.Type == t && c.Status == corev1.ConditionTrue {
			return c
		}
	}
	return nil
}

func jobOutcome(job *batchv1.Job) batchv1.JobConditionType {
	if jobCondition(job, batchv1.JobComplete) != nil {
		return batchv1.JobComplete
	}
	if jobCondition(job, batchv1.JobFailed) != nil {
		return batchv1.JobFailed
	}
	return ""
}

// errorTail is the last few lines Trivy printed, for the failure message.
func (w *Worker) errorTail(ctx context.Context, jobName string) string {
	logs, err := w.readLogs(ctx, jobName)
	if err != nil {
		return ""
	}
	var keep []string
	for _, l := range strings.Split(logs, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			keep = append(keep, l)
		}
	}
	if len(keep) > 3 {
		keep = keep[len(keep)-3:]
	}
	return clip(strings.Join(keep, " | "), 1000)
}

func (w *Worker) readLogs(ctx context.Context, jobName string) (string, error) {
	if w.Logs != nil {
		return w.Logs(ctx, w.Namespace, jobName)
	}
	pods, err := w.Kube.CoreV1().Pods(w.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "job-name=" + jobName})
	if err != nil {
		return "", fmt.Errorf("list scan pods: %w", err)
	}
	if len(pods.Items) == 0 {
		return "", errors.New("scan pod is gone")
	}
	pick := pods.Items[0]
	for _, p := range pods.Items[1:] {
		if p.CreationTimestamp.After(pick.CreationTimestamp.Time) {
			pick = p
		}
	}
	limit := int64(maxLogBytes)
	rc, err := w.Kube.CoreV1().Pods(w.Namespace).GetLogs(pick.Name, &corev1.PodLogOptions{Container: container, LimitBytes: &limit}).Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("stream scan logs: %w", err)
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, limit))
	if err != nil {
		return "", fmt.Errorf("read scan logs: %w", err)
	}
	return string(b), nil
}

func (w *Worker) ensureCachePVC(ctx context.Context) error {
	pvcs := w.Kube.CoreV1().PersistentVolumeClaims(w.Namespace)
	if _, err := pvcs.Get(ctx, CachePVC, metav1.GetOptions{}); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	if _, err := pvcs.Create(ctx, buildCachePVC(w.Namespace), metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// credentials returns a docker config for host from the
// "<namespace>/<name>" dockerconfigjson Secret, or nil.
func (w *Worker) credentials(ctx context.Context, ref, host string) ([]byte, error) {
	if ref == "" {
		return nil, nil
	}
	ns, name, ok := strings.Cut(ref, "/")
	if !ok || ns == "" || name == "" {
		return nil, fmt.Errorf("bad pull secret reference %q", ref)
	}
	sec, err := w.Kube.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("read pull secret %s: %w", ref, err)
	}
	raw := sec.Data[corev1.DockerConfigJsonKey]
	if len(raw) == 0 {
		return nil, fmt.Errorf("pull secret %s has no %s", ref, corev1.DockerConfigJsonKey)
	}
	cfg, found, err := hostDockerConfig(raw, host)
	if err != nil || !found {
		return nil, err
	}
	return cfg, nil
}

func (w *Worker) cleanupAuth(ctx context.Context, scanID int64) {
	err := w.Kube.CoreV1().Secrets(w.Namespace).Delete(ctx, authSecretName(scanID), metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		w.log().Warn("vulnscan: delete scan credentials (the Job's TTL will collect it)", "scan", scanID, "err", err)
	}
}

func (w *Worker) deleteJob(ctx context.Context, name string) {
	bg := metav1.DeletePropagationBackground
	if err := w.Kube.BatchV1().Jobs(w.Namespace).Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &bg}); err != nil && !apierrors.IsNotFound(err) {
		w.log().Warn("vulnscan: delete scan job", "job", name, "err", err)
	}
}
