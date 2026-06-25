// Package logs reads recent log lines from the pods backing a service's
// environment. Read-only one-shot tail; streaming lands later behind a
// websocket route.
//
// Pod selector mirrors the helm chart's label:
// app.kubernetes.io/instance=<env-name>.
package logs

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// BuildLogReader is the read-side of the BuildLog archive (db.DB).
// Optional dependency: if non-nil, Stream will fall back to the
// archive when a "build:<id>" stream request finds no live pod.
type BuildLogReader interface {
	GetBuildLog(ctx context.Context, buildName string) (string, error)
}

// Service handles log reads. Construct via New.
type Service struct {
	Kube       *kube.Client
	Namespace  string
	NSResolver *kube.ProjectNamespaceResolver
	// BuildLogs is the persisted-tail fallback for build:<id> streams
	// after the kaniko Job pod has been TTL'd. Nil = no fallback (the
	// stream returns the "pod not found" message as before).
	BuildLogs BuildLogReader
}

// New constructs a logs.Service.
func New(k *kube.Client, namespace string) *Service {
	if namespace == "" {
		namespace = "kuso"
	}
	return &Service{Kube: k, Namespace: namespace}
}

func (s *Service) nsFor(ctx context.Context, project string) string {
	if s.NSResolver == nil || project == "" {
		return s.Namespace
	}
	return s.NSResolver.NamespaceFor(ctx, project)
}

// Errors mirroring sibling packages.
var (
	ErrNotFound = errors.New("logs: env not found")
)

// Line is one decoded log line, tagged with the pod it came from.
type Line struct {
	Pod  string `json:"pod"`
	Line string `json:"line"`
}

// Tail returns up to lines log lines combined across pods, balancing per
// pod. env="" defaults to "production". env may be either the short name
// ("production", "preview-pr-7") or the fully-qualified env CR name.
func (s *Service) Tail(ctx context.Context, project, service, env string, lines int) ([]Line, string, error) {
	if lines <= 0 {
		lines = 200
	}
	if lines > 2000 {
		lines = 2000
	}
	if env == "" {
		env = "production"
	}
	fqn := service
	if !strings.HasPrefix(service, project+"-") {
		fqn = project + "-" + service
	}
	ns := s.nsFor(ctx, project)

	// build:<KusoBuild name> — same shape the streaming WS endpoint
	// accepts. Without this branch, the REST tail tried to look up
	// an env CR named "build:<id>" and 404'd, even though the
	// build's pods are right there + the persisted-tail fallback is
	// wired. CLI users hitting `kuso logs <p> <s> --env build:<id>`
	// without -f got "server returned 404: not found" with no
	// recourse short of switching to --follow.
	if strings.HasPrefix(env, "build:") {
		buildName := strings.TrimPrefix(env, "build:")
		out, err := s.tailBuildPods(ctx, ns, buildName, lines)
		if err == nil && len(out) > 0 {
			return out, env, nil
		}
		// Pods gone (helm uninstalled the build). Fall back to the
		// archived tail in postgres.
		if s.BuildLogs != nil {
			archived, err := s.BuildLogs.GetBuildLog(ctx, buildName)
			if err == nil && archived != "" {
				return archivedTextToLines(archived, lines), env, nil
			}
		}
		return []Line{}, env, nil
	}

	// run:<KusoRun name> — same shape as build:<...> but for one-shot
	// task pods. The run pod's label kuso.sislelabs.com/run=<name>
	// (set by the kusorun helm chart's _helpers.tpl) is the selector.
	// Once the Job's ttlSecondsAfterFinished elapses (~10 min default)
	// the pod is GC'd; we don't have a persisted-archive fallback the
	// way builds do, so post-TTL we return empty + the caller's UI
	// shows "log output no longer available" rather than 404.
	if strings.HasPrefix(env, "run:") {
		runName := strings.TrimPrefix(env, "run:")
		out, err := s.tailRunPods(ctx, ns, runName, lines)
		if err == nil {
			return out, env, nil
		}
		return []Line{}, env, nil
	}

	envName := env
	if !strings.Contains(env, "-") {
		envName = fqn + "-" + env
	}

	// Verify the env exists — saves the caller a confusing empty result
	// when they typo a name. AND verify it actually belongs to the
	// (project, service) the caller authorized against; without that
	// check, ?env=other-project-svc-prod returns logs from a service
	// the caller has no access to. The env CR's spec is authoritative.
	envCR, err := s.Kube.GetKusoEnvironment(ctx, ns, envName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, envName, ErrNotFound
		}
		return nil, envName, fmt.Errorf("get env: %w", err)
	}
	// Tenancy gate. The previous `!= ""` shape let a zero-valued env
	// (legacy CR / decode failure) bypass the check entirely.
	if envCR.Spec.Project != project || envCR.Spec.Service != fqn {
		return nil, envName, ErrNotFound
	}

	pods, err := s.Kube.Clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: kube.LabelSelector(map[string]string{"app.kubernetes.io/instance": envName}),
	})
	if err != nil {
		return nil, envName, fmt.Errorf("list pods: %w", err)
	}
	if len(pods.Items) == 0 {
		return []Line{}, envName, nil
	}
	perPod := int64(lines / len(pods.Items))
	if perPod < 1 {
		perPod = 1
	}

	out := make([]Line, 0, lines)
	for i := range pods.Items {
		pod := &pods.Items[i]
		podLines, err := s.tailOnePod(ctx, ns, pod, perPod)
		if err != nil {
			// Skip the pod rather than failing the whole tail — partial
			// data beats no data when one container is restarting.
			continue
		}
		out = append(out, podLines...)
	}
	if len(out) > lines {
		out = out[len(out)-lines:]
	}
	return out, envName, nil
}

// tailOnePod issues a single GetLogs request against the pod's primary
// container and returns the parsed lines. Container selection is left
// implicit — the kuso operator only renders one container per pod, and
// the kube API will pick the only one.
//
// When the container has restarted (it crashed and kube relaunched it),
// kubelet only serves the CURRENT container's logs by default — the logs
// from the run that actually crashed are gone. "What did it log right
// before it OOM'd?" is then unanswerable. So if the pod shows restarts we
// also pull the PREVIOUS container's tail (PodLogOptions{Previous:true})
// and prepend it behind a separator line, best-effort.
func (s *Service) tailOnePod(ctx context.Context, ns string, pod *corev1.Pod, tailLines int64) ([]Line, error) {
	out := make([]Line, 0, tailLines)

	// Previous-container logs first (chronologically older), only when the
	// pod has restarted — skip the extra apiserver round-trip otherwise.
	if podRestartTotal(pod) > 0 {
		if prev := s.tailPodStream(ctx, ns, pod, tailLines, true); len(prev) > 0 {
			out = append(out, prev...)
			out = append(out, Line{Pod: pod.Name, Line: "── pod restarted; logs below are from the current container ──"})
		}
	}

	out = append(out, s.tailPodStream(ctx, ns, pod, tailLines, false)...)
	return out, nil
}

// podRestartTotal sums RestartCount across the pod's containers. >0 means
// at least one container crashed and was relaunched, so the previous
// container's logs are worth fetching.
func podRestartTotal(p *corev1.Pod) int32 {
	var total int32
	for _, cs := range p.Status.ContainerStatuses {
		total += cs.RestartCount
	}
	return total
}

// tailPodStream streams one GetLogs request (current or previous
// container) and returns its lines. Errors are swallowed to nil so the
// previous-container probe never fails the whole request (e.g. no prior
// container exists yet) — the caller decides what to do with an empty
// result.
func (s *Service) tailPodStream(ctx context.Context, ns string, pod *corev1.Pod, tailLines int64, previous bool) []Line {
	req := s.Kube.Clientset.CoreV1().Pods(ns).GetLogs(pod.Name, &corev1.PodLogOptions{
		TailLines: &tailLines,
		Previous:  previous,
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		return nil
	}
	defer stream.Close()

	out := make([]Line, 0, tailLines)
	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		out = append(out, Line{Pod: pod.Name, Line: line})
	}
	return out
}

// tailBuildPods returns lines from every pod owned by a KusoBuild,
// honoring init containers in order (clone → env-detect → optional
// nixpacks-plan → kaniko). Mirrors the WS streamer's pod selection.
// Returns ([], nil) when no pods exist (caller falls back to the
// persisted archive).
func (s *Service) tailBuildPods(ctx context.Context, ns, buildName string, lines int) ([]Line, error) {
	pods, err := s.Kube.Clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: kube.LabelSelector(map[string]string{"app.kubernetes.io/instance": buildName}),
	})
	if err != nil {
		return nil, fmt.Errorf("list build pods: %w", err)
	}
	if len(pods.Items) == 0 {
		return nil, nil
	}
	tail := int64(lines)
	if len(pods.Items) > 1 {
		tail = int64(lines / len(pods.Items))
		if tail < 1 {
			tail = 1
		}
	}
	out := make([]Line, 0, lines)
	for i := range pods.Items {
		pod := &pods.Items[i]
		// Pull each container individually — init failures (clone
		// fatal, missing token) live on the init pod's stdout and
		// would otherwise be lost if we only read the main one.
		for _, c := range append(append([]string{}, containerNames(pod.Spec.InitContainers)...), containerNames(pod.Spec.Containers)...) {
			req := s.Kube.Clientset.CoreV1().Pods(ns).GetLogs(pod.Name, &corev1.PodLogOptions{
				Container: c,
				TailLines: &tail,
			})
			stream, err := req.Stream(ctx)
			if err != nil {
				continue
			}
			scanner := bufio.NewScanner(stream)
			scanner.Buffer(make([]byte, 64*1024), 1024*1024)
			for scanner.Scan() {
				line := scanner.Text()
				if line == "" {
					continue
				}
				out = append(out, Line{Pod: pod.Name + "/" + c, Line: line})
			}
			stream.Close()
		}
	}
	if len(out) > lines {
		out = out[len(out)-lines:]
	}
	return out, nil
}

// tailRunPods is the run-pod analog of tailBuildPods. Selects on
// kuso.sislelabs.com/run=<name> (the label the kusorun helm chart
// stamps via _helpers.tpl). One-shot Jobs only ever produce one
// pod, so the per-pod balancing the build version does for
// init-container chains isn't needed; we tail the single container
// the run-pod has.
//
// Returns ([], nil) when no pod is found — typically because the
// Job's TTL has elapsed and kube garbage-collected the pod. The
// caller's UI shows "log output no longer available" rather than a
// confusing error.
func (s *Service) tailRunPods(ctx context.Context, ns, runName string, lines int) ([]Line, error) {
	pods, err := s.Kube.Clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: kube.LabelSelector(map[string]string{"kuso.sislelabs.com/run": runName}),
	})
	if err != nil {
		return nil, fmt.Errorf("list run pods: %w", err)
	}
	if len(pods.Items) == 0 {
		return []Line{}, nil
	}
	tail := int64(lines)
	out := make([]Line, 0, lines)
	for i := range pods.Items {
		pod := &pods.Items[i]
		// Single container per run pod (the kusorun chart renders
		// `containers: [{name: run, …}]`), so we just GetLogs without
		// a Container override. If the user's command wrote to stderr
		// only, kubelet aggregates both streams into the same pipe.
		req := s.Kube.Clientset.CoreV1().Pods(ns).GetLogs(pod.Name, &corev1.PodLogOptions{
			TailLines: &tail,
		})
		stream, err := req.Stream(ctx)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(stream)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			out = append(out, Line{Pod: pod.Name, Line: line})
		}
		stream.Close()
	}
	if len(out) > lines {
		out = out[len(out)-lines:]
	}
	return out, nil
}

// archivedTextToLines converts a saved build-log blob (one big string,
// container-prefix headers + line-feeds) into the same Line shape the
// pod-tail path emits. The persisted archive is best-effort — we
// don't have per-line pod attribution, so all lines carry an
// "<archive>" pod tag so users see the source.
func archivedTextToLines(text string, max int) []Line {
	lines := strings.Split(text, "\n")
	if len(lines) > max {
		lines = lines[len(lines)-max:]
	}
	out := make([]Line, 0, len(lines))
	for _, l := range lines {
		if l == "" {
			continue
		}
		out = append(out, Line{Pod: "<archive>", Line: l})
	}
	return out
}

// containerNames returns the .Name field of a container slice. Local
// helper so callers don't grow their own one-liner.
func containerNames(in []corev1.Container) []string {
	out := make([]string, len(in))
	for i := range in {
		out[i] = in[i].Name
	}
	return out
}
