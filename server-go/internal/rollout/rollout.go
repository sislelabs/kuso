// Package rollout reads a Deployment caught between two ReplicaSets: is
// the new version failing while the previous one still serves, and when
// did the current ReplicaSet finish rolling out. Pure functions over pod
// and Deployment snapshots, shared by the service-state rollup and the
// alert engine.
package rollout

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
	"kuso/server/internal/podreason"
)

// Rollout-failure kinds (status.rollout.kind). Wire strings: add, never rename.
const (
	KindHealthcheck = "healthcheck"
	KindCrash       = "crash"
	KindImagePull   = "image_pull"
	KindConfig      = "config_error"
	KindStalled     = "stalled"
)

// Facts describes a Deployment caught between two ReplicaSets.
// Zero value = no mixed rollout in flight.
type Facts struct {
	// InFlight: pods from more than one ReplicaSet exist, i.e. the
	// previous version hasn't been fully replaced yet.
	InFlight bool
	// PreviousServing: at least one pod of an older ReplicaSet is ready.
	PreviousServing bool
	// PreviousSince is when the newest ready pod of an older ReplicaSet
	// started: roughly when the version still serving went out.
	PreviousSince time.Time
	// Failing: the new ReplicaSet's pods are failing, not just starting.
	Failing bool
	Kind    string
	// Cause is the human reason without the "previous version" tail.
	Cause string
	// NewPod is the failing new pod (for the probe-event lookup).
	NewPod string
	Since  time.Time
	// NewImage is what the rollout moves to; PreviousImage what the
	// version still serving runs. Either is "" when no pod says.
	NewImage      string
	PreviousImage string
	// PullCause is the registry's answer for KindImagePull (Pull* consts).
	PullCause string
}

// Registry answers for a failed image pull (status.rollout.pullCause).
// Wire strings: add, never rename.
const (
	PullNotFound     = "not_found"
	PullUnauthorized = "unauthorized"
	PullUnreachable  = "unreachable"
)

var pullStatusRE = regexp.MustCompile(`\b40[13]\b`)

// PullCause reads why a pull failed from the kubelet's ErrImagePull text
// or a "Failed to pull image" event. ImagePullBackOff's own message
// ("Back-off pulling image …") carries no cause, so it returns "".
// Docker Hub answers a missing repository with "pull access denied …
// may require authorization", which lands on unauthorized; the hint for
// it names both possibilities.
func PullCause(message string) string {
	m := strings.ToLower(message)
	has := func(needles ...string) bool {
		for _, n := range needles {
			if strings.Contains(m, n) {
				return true
			}
		}
		return false
	}
	switch {
	case pullStatusRE.MatchString(m), has("unauthorized", "forbidden", "access denied", "authorization failed", "insufficient_scope", "authentication required"):
		return PullUnauthorized
	case has("not found", "manifest unknown", "404"):
		return PullNotFound
	case has("no such host", "i/o timeout", "connection refused", "tls handshake", "deadline exceeded"):
		return PullUnreachable
	}
	return ""
}

// PullFailureCause is the human cause of a failed pull of image, by the
// registry's answer.
func PullFailureCause(pullCause, image string) string {
	switch pullCause {
	case PullNotFound:
		if tag := ImageTag(image); tag != "" {
			return fmt.Sprintf("the new version's image tag %s wasn't found in the registry", tag)
		}
		return "the new version's image wasn't found in the registry"
	case PullUnauthorized:
		return "the registry refused to serve the new version's image (401/403)"
	case PullUnreachable:
		return "the registry for the new version's image can't be reached"
	}
	return "the new version's image can't be pulled"
}

// ImageTag is the tag of an image reference ("repo:tag" → "tag"), a
// digest's first 12 hex characters, or "" for an untagged reference.
func ImageTag(ref string) string {
	if i := strings.LastIndex(ref, "@"); i >= 0 {
		d := strings.TrimPrefix(ref[i+1:], "sha256:")
		if len(d) > 12 {
			d = d[:12]
		}
		return d
	}
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		return ref[i+1:]
	}
	return ""
}

// AppImage is the image of the env's app container (named "app" by the
// kusoenvironment chart; the first container on anything older).
func AppImage(spec *corev1.PodSpec) string {
	if spec == nil || len(spec.Containers) == 0 {
		return ""
	}
	for _, c := range spec.Containers {
		if c.Name == "app" {
			return c.Image
		}
	}
	return spec.Containers[0].Image
}

// PendingApply: the env asks for want but the Deployment's template still
// runs another image, so the operator hasn't applied the change yet and
// every pod is still the previous version. Returns the template's image.
func PendingApply(dep *appsv1.Deployment, want string) (string, bool) {
	if dep == nil || want == "" {
		return "", false
	}
	cur := AppImage(&dep.Spec.Template.Spec)
	return cur, cur != "" && cur != want
}

// Analyze splits the env's pods by pod-template-hash. The newest
// group is the new ReplicaSet (a rollout creates it after the old one);
// a recreated old pod could in theory be younger, which only delays the
// verdict until the deadline condition flips. Pure, table-tested.
func Analyze(pods []*corev1.Pod, hc *kube.KusoHealthcheck, deadlineExceeded bool, now time.Time) Facts {
	type group struct {
		pods   []*corev1.Pod
		newest time.Time
	}
	groups := map[string]*group{}
	for _, p := range pods {
		if p == nil || p.DeletionTimestamp != nil || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		if owner := metav1.GetControllerOf(p); owner != nil && owner.Kind == "Job" {
			continue
		}
		h := p.Labels["pod-template-hash"]
		if h == "" {
			continue
		}
		g := groups[h]
		if g == nil {
			g = &group{}
			groups[h] = g
		}
		g.pods = append(g.pods, p)
		if t := p.CreationTimestamp.Time; t.After(g.newest) {
			g.newest = t
		}
	}
	if len(groups) < 2 {
		return Facts{}
	}
	hashes := make([]string, 0, len(groups))
	for h := range groups {
		hashes = append(hashes, h)
	}
	sort.Slice(hashes, func(i, j int) bool { return groups[hashes[i]].newest.After(groups[hashes[j]].newest) })
	out := Facts{InFlight: true, NewImage: AppImage(&groups[hashes[0]].pods[0].Spec)}
	for _, h := range hashes[1:] {
		for _, p := range groups[h].pods {
			if podIsReady(p) {
				out.PreviousServing = true
				if t := p.CreationTimestamp.Time; !t.Before(out.PreviousSince) {
					out.PreviousSince = t
					out.PreviousImage = AppImage(&p.Spec)
				}
			}
		}
	}

	// Health-check grace: the probe gets initialDelay + period×threshold
	// to pass before a not-ready new pod counts as failing it.
	hcGrace := time.Duration(0)
	if hc != nil && hc.Path != "" {
		hcGrace = time.Duration(orDefault(hc.InitialDelaySeconds, 5)+orDefault(hc.PeriodSeconds, 10)*orDefault(hc.FailureThreshold, 3))*time.Second + 15*time.Second
	}
	for _, p := range groups[hashes[0]].pods {
		if podIsReady(p) {
			continue
		}
		age := now.Sub(p.CreationTimestamp.Time)
		restarts := int32(0)
		exitCode := int32(-1)
		for _, cs := range p.Status.ContainerStatuses {
			restarts += cs.RestartCount
			if t := cs.LastTerminationState.Terminated; t != nil {
				exitCode = t.ExitCode
			}
		}
		issue := podreason.ForPod(p, now)
		f := out
		f.NewPod, f.Since = p.Name, p.CreationTimestamp.Time
		switch {
		case issue != nil && issue.Kind == podreason.KindImagePull:
			f.PullCause = PullCause(issue.Message)
			f.Kind, f.Cause = KindImagePull, PullFailureCause(f.PullCause, f.NewImage)
		case issue != nil && (issue.Kind == podreason.KindConfig || issue.Kind == podreason.KindUnschedulable):
			f.Kind, f.Cause = KindConfig, "the new version can't start: "+lowerFirst(issue.Summary)
		case hcGrace > 0 && (restarts > 0 || age > hcGrace):
			f.Kind, f.Cause = KindHealthcheck, fmt.Sprintf("the new version fails its health check (GET %s)", hc.Path)
		case restarts > 0 && (issue != nil || exitCode != 0):
			f.Kind, f.Cause = KindCrash, "the new version keeps crashing on start"
			if exitCode > 0 {
				f.Cause += fmt.Sprintf(" (exit code %d)", exitCode)
			}
		case issue != nil:
			f.Kind, f.Cause = KindStalled, "the new version "+strings.TrimPrefix(lowerFirst(issue.Summary), "the new instance ")
		case deadlineExceeded:
			f.Kind, f.Cause = KindStalled, fmt.Sprintf("the new version hasn't become ready in %s", roundMinutes(age))
		default:
			continue
		}
		f.Failing = true
		return f
	}
	return out
}

func podIsReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func orDefault(v, def int32) int32 {
	if v > 0 {
		return v
	}
	return def
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

func roundMinutes(d time.Duration) string {
	if m := int(d.Minutes()); m >= 1 {
		return fmt.Sprintf("%d min", m)
	}
	return fmt.Sprintf("%d s", int(d.Seconds()))
}

func DeadlineExceeded(dep *appsv1.Deployment) bool {
	if dep == nil {
		return false
	}
	for _, c := range dep.Status.Conditions {
		if c.Type == appsv1.DeploymentProgressing && c.Reason == "ProgressDeadlineExceeded" {
			return true
		}
	}
	return false
}

// CompletedAt is when the Deployment's current ReplicaSet became
// fully available; zero while a rollout is in flight or never finished.
func CompletedAt(dep *appsv1.Deployment) time.Time {
	if dep == nil {
		return time.Time{}
	}
	for _, c := range dep.Status.Conditions {
		if c.Type == appsv1.DeploymentProgressing && c.Status == corev1.ConditionTrue && c.Reason == "NewReplicaSetAvailable" {
			if !c.LastUpdateTime.IsZero() {
				return c.LastUpdateTime.Time
			}
			return c.LastTransitionTime.Time
		}
	}
	return time.Time{}
}

// ProbeOutcome pulls the HTTP result out of a kubelet probe event
// ("Readiness probe failed: HTTP probe failed with statuscode: 404",
// "… connect: connection refused", "… context deadline exceeded").
var probeStatusRE = regexp.MustCompile(`statuscode: (\d{3})`)

func ProbeOutcome(message string) string {
	if m := probeStatusRE.FindStringSubmatch(message); m != nil {
		return m[1]
	}
	low := strings.ToLower(message)
	switch {
	case strings.Contains(low, "connection refused"):
		return "connection refused"
	case strings.Contains(low, "deadline exceeded"), strings.Contains(low, "timeout"):
		return "timed out"
	}
	return ""
}
