package builds

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/failures"
	"kuso/server/internal/kube"
	"kuso/server/internal/podreason"
)

// BuildStallAfter is how long an in-flight build may go without its pod
// running before the UI is told why. Cloning starts within seconds once
// the pod is scheduled and the builder image is present.
const BuildStallAfter = 60 * time.Second

// Stall kinds cross the wire (buildSummary.stalled.kind).
const (
	StallImagePull     = "image_pull"
	StallUnschedulable = "unschedulable"
	StallConfig        = "config_error"
	StallNoPod         = "no_pod"
	StallStarting      = "starting"
)

// KindBuildPlatform classifies a build kuso failed because its own build
// pod never got going (builder image, scheduling, pod config): nothing in
// the user's repo caused it. Crosses the wire as failureClass.kind.
const KindBuildPlatform failures.Kind = "build_platform"

// PlatformFailurePrefix starts the message of every build failed for a
// platform reason. Archived build records keep only the message, so the
// UI matches on it when the classification is gone.
const PlatformFailurePrefix = "kuso couldn't start the build"

// Stall says why an in-flight build isn't making progress.
type Stall struct {
	Kind    string    `json:"kind"`
	Summary string    `json:"summary"`
	Detail  string    `json:"detail,omitempty"`
	Since   time.Time `json:"since"`
	// Container is the build pod container that's stuck (env-detect,
	// buildkit, …) when the kubelet names one, so the UI marks that step
	// rather than the one before it.
	Container string `json:"container,omitempty"`
	// FailsAt is when the poller fails the build if it's still stuck.
	FailsAt time.Time `json:"failsAt,omitzero"`
}

// StallFor explains a build whose pod hasn't started running BuildStallAfter
// after the build began, or returns nil when it's progressing (or too
// early to say). pods are the build Job's pods.
func StallFor(pods []corev1.Pod, started, now time.Time) *Stall {
	if started.IsZero() || now.Sub(started) < BuildStallAfter {
		return nil
	}
	if len(pods) == 0 {
		return &Stall{Kind: StallNoPod, Summary: "The build hasn't started: no build pod was created", Since: started}
	}
	p := &pods[0]
	for i := 1; i < len(pods); i++ {
		if pods[i].CreationTimestamp.After(p.CreationTimestamp.Time) {
			p = &pods[i]
		}
	}
	if p.Status.Phase != corev1.PodPending {
		return nil
	}
	since := p.CreationTimestamp.Time
	if since.IsZero() {
		since = started
	}
	for _, list := range [][]corev1.ContainerStatus{p.Status.InitContainerStatuses, p.Status.ContainerStatuses} {
		for i := range list {
			// The clone step is an init container: the pod stays Pending
			// while it runs, which is progress.
			if list[i].State.Running != nil {
				return nil
			}
			w := list[i].State.Waiting
			if w == nil {
				continue
			}
			raw := w.Reason
			if w.Message != "" {
				raw += ": " + w.Message
			}
			switch w.Reason {
			case "ImagePullBackOff", "ErrImagePull", "InvalidImageName":
				return &Stall{Kind: StallImagePull, Summary: "Builder image can't be pulled", Detail: raw, Since: since, Container: list[i].Name}
			case "CreateContainerConfigError", "CreateContainerError", "RunContainerError":
				return &Stall{Kind: StallConfig, Summary: "The build pod can't start: " + lowerFirst(podreason.Summarize(w.Reason, w.Message)), Detail: raw, Since: since, Container: list[i].Name}
			}
		}
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse {
			return &Stall{
				Kind:    StallUnschedulable,
				Summary: "Waiting for a node: " + lowerFirst(podreason.SummarizeScheduling(c.Message)),
				Detail:  strings.TrimSpace(c.Reason + ": " + c.Message),
				Since:   since,
			}
		}
	}
	return &Stall{Kind: StallStarting, Summary: "The build pod is still starting (downloading the builder image or waiting on storage)", Detail: "pod Pending", Since: since}
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// Stalls checks every in-flight build in bs and returns the stalled ones
// by name. Only builds past BuildStallAfter cost a pod list.
func (s *Service) Stalls(ctx context.Context, bs []kube.KusoBuild, now time.Time) map[string]*Stall {
	out := map[string]*Stall{}
	if s == nil || s.Kube == nil || s.Kube.Clientset == nil {
		return out
	}
	for i := range bs {
		b := &bs[i]
		phase := b.Annotations[annPhase]
		if phase != "pending" && phase != "running" {
			continue
		}
		// pending = Job dispatched; running = the Job has a pod, which
		// may still be unscheduled or unable to pull the builder image.
		started := stallClock(b)
		if started.IsZero() || now.Sub(started) < BuildStallAfter {
			continue
		}
		pods, err := s.buildPods(ctx, b.Namespace, b.Name)
		if err != nil {
			continue
		}
		if st := StallFor(pods, started, now); st != nil {
			st.FailsAt = st.Since.Add(stallLimit(st.Kind, buildStartTimeout()))
			out[b.Name] = st
		}
	}
	return out
}

// stallClock is when the build's current attempt began: the latest of
// dispatch and start, else CR creation.
func stallClock(b *kube.KusoBuild) time.Time {
	started := b.CreationTimestamp.Time
	for _, ann := range []string{annDispatchedAt, annStartedAt} {
		if t, err := time.Parse(time.RFC3339, b.Annotations[ann]); err == nil {
			started = t
		}
	}
	return started
}

func (s *Service) buildPods(ctx context.Context, ns, build string) ([]corev1.Pod, error) {
	lctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	pods, err := s.Kube.Clientset.CoreV1().Pods(ns).List(lctx, metav1.ListOptions{
		LabelSelector: kube.LabelSelector(map[string]string{"app.kubernetes.io/instance": build}),
	})
	if err != nil {
		return nil, err
	}
	return pods.Items, nil
}

// buildStartTimeout is how long a build pod may sit unable to pull its
// builder image or start a container before the poller fails the build.
// Waiting for a node or a slow first pull of a large builder image gets
// twice as long. Override with KUSO_BUILD_START_TIMEOUT (Go duration).
func buildStartTimeout() time.Duration {
	if v := os.Getenv("KUSO_BUILD_START_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 10 * time.Minute
}

// stallLimit is how long a stall of kind may last before the build fails.
func stallLimit(kind string, limit time.Duration) time.Duration {
	switch kind {
	case StallImagePull, StallConfig:
		return limit
	}
	return 2 * limit
}

// stallExpired reports whether st has lasted long enough to give up.
func stallExpired(st *Stall, now time.Time, limit time.Duration) bool {
	if st == nil || st.Since.IsZero() {
		return false
	}
	return now.Sub(st.Since) >= stallLimit(st.Kind, limit)
}

// platformFailure turns an expired stall into the failed build's message
// and classification. The message ends in "(<container>)" when one is
// known, which is how the UI names the step a build failed at.
func platformFailure(st *Stall, now time.Time) (string, failures.Classification) {
	what, fix := "the build pod never started", "the cluster can run the build pod again"
	switch st.Kind {
	case StallImagePull:
		what = "builder image unavailable"
		fix = "the node can pull the builder image (a registry outage, or no image for the node's CPU architecture)"
	case StallConfig:
		what = "the build pod is misconfigured"
		fix = "the build pod's missing secret or config is restored"
	case StallUnschedulable:
		what = "no node can run it"
		fix = "a node has room for the build (free capacity, or check placement labels)"
	}
	summary := PlatformFailurePrefix + ": " + what
	waited := now.Sub(st.Since).Round(time.Minute)
	msg := fmt.Sprintf("%s after %s", summary, waited)
	if st.Detail != "" {
		msg += ". " + st.Detail
	}
	if st.Container != "" {
		msg += " (" + st.Container + ")"
	}
	return msg, failures.Classification{
		Kind:    KindBuildPlatform,
		Tab:     failures.TabLogs,
		Summary: summary,
		Remediation: &failures.Remediation{
			Title: "Not a problem with your code",
			Detail: fmt.Sprintf("%s for %s, so kuso stopped the build. Retrying fails the same way until %s; an admin can see the cause with `kuso get events`.",
				st.Summary, waited, fix),
		},
	}
}

// failStalledStart fails a build whose pod has been stuck before running
// any user code for longer than buildStartTimeout, and reports whether it
// did. Builds with a ready pod never cost a pod list.
func (p *Poller) failStalledStart(ctx context.Context, ns string, b *kube.KusoBuild, readyPods *int32, now time.Time) (bool, error) {
	limit := buildStartTimeout()
	started := stallClock(b)
	if started.IsZero() || now.Sub(started) < limit || (readyPods != nil && *readyPods > 0) {
		return false, nil
	}
	pods, err := p.Svc.buildPods(ctx, ns, b.Name)
	if err != nil {
		return false, nil
	}
	st := StallFor(pods, started, now)
	if !stallExpired(st, now, limit) {
		return false, nil
	}
	msg, class := platformFailure(st, now)
	p.logger().Warn("build failed: build pod never started", "build", b.Name, "ns", ns, "stall", st.Kind, "detail", st.Detail)
	if err := p.markFailedClassified(ctx, ns, b, msg, &class); err != nil {
		return true, err
	}
	p.deleteJobAfter(ns, b.Name, startTimeoutJobGrace)
	return true, nil
}

// startTimeoutJobGrace leaves the failed build's pod in place long enough
// for the archive worker to read its logs (the clone output); then the Job
// goes so the kubelet stops retrying the pull.
var startTimeoutJobGrace = 30 * time.Second

func (p *Poller) deleteJobAfter(ns, name string, grace time.Duration) {
	time.AfterFunc(grace, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		bg := metav1.DeletePropagationBackground
		if err := p.Svc.Kube.Clientset.BatchV1().Jobs(ns).Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &bg}); err != nil && !apierrors.IsNotFound(err) {
			p.logger().Warn("delete stalled build job", "build", name, "ns", ns, "err", err)
		}
	})
}

// isPlatformFailure reports whether b was failed by failStalledStart.
func isPlatformFailure(b *kube.KusoBuild) bool {
	var c failures.Classification
	return json.Unmarshal([]byte(b.Annotations[annClassification]), &c) == nil && c.Kind == KindBuildPlatform
}
