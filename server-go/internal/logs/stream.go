// Package logs streaming: WebSocket-driven follow tail across all pods
// in an environment. Each pod gets a goroutine that reads from the kube
// log stream line-by-line and forwards JSON frames to the multiplexer
// channel. The handler reads from that channel and writes WS frames
// until the client disconnects or the context expires.
//
// The frame envelope mirrors what the frontend expects:
//
//	{ "type": "log",   "pod": "...", "stream": "stdout", "line": "...", "ts": "RFC3339" }
//	{ "type": "phase", "value": "BUILDING" }   // future
//	{ "type": "ping" }                          // server heartbeat
//	{ "type": "error", "message": "..." }
//
// Auth is handled by the caller — by the time Stream is invoked, the
// upgrade has succeeded and the user has a valid bearer (or
// kuso.JWT_TOKEN cookie). See handlers/logs_ws.go.
package logs

import (
	"bufio"
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Caps. maxTailLinesPerPod bounds the historical tail kube returns
// for any one pod; maxAggregateTailFrames bounds the total backlog
// across every pod in a single stream session before we drop with
// a warning. Tuned for a 768 MiB pod and a 50-pod env: 50 × 1000 ×
// ~4 KB ≈ 200 MB worst-case, which fits even with the sink stalled.
const (
	maxTailLinesPerPod     = 1000
	maxAggregateTailFrames = 25000
)

// Frame is the JSON-serialised envelope written to the WS connection.
type Frame struct {
	Type    string `json:"type"`
	Pod     string `json:"pod,omitempty"`
	Stream  string `json:"stream,omitempty"`
	Line    string `json:"line,omitempty"`
	Ts      string `json:"ts,omitempty"`
	Value   string `json:"value,omitempty"`
	Message string `json:"message,omitempty"`
}

// Sink is the writer side of a streaming session. The handler implements
// it (writes frames to a WS connection); tests can supply a buffered
// channel.
type Sink interface {
	Write(f Frame) error
}

// Stream tails logs from every pod in the env, sending frames to sink
// until ctx is canceled or all pods finish. Returns the resolved env CR
// name (so handlers can echo it back) and an error if the env or pods
// aren't reachable.
//
// follow=true uses kube's Follow streaming; tailLines pre-loads the
// last N lines per pod from before the follow began (mirrors `kubectl
// logs -f --tail`).
//
// Caps:
//   - per-pod: 1000 lines (was 5000). 5000 × 50 pods × ~4 KB/line was
//     ~1 GB buffered before the first frame shipped — enough to OOM
//     a 768 MiB pod with one greedy client.
//   - aggregate: streamPods enforces a maxAggregateTailFrames ceiling
//     across all pod goroutines combined so 100 pods × 1000 doesn't
//     blow memory either. New frames after the cap is reached drop
//     with a one-shot warning frame instead of silently truncating.
func (s *Service) Stream(ctx context.Context, project, service, env string, tailLines int, sink Sink) (string, error) {
	if tailLines <= 0 {
		tailLines = 100
	}
	if tailLines > maxTailLinesPerPod {
		tailLines = maxTailLinesPerPod
	}
	if env == "" {
		env = "production"
	}
	fqn := service
	if !strings.HasPrefix(service, project+"-") {
		fqn = project + "-" + service
	}

	// Build-pod stream: env="build:<KusoBuild name>". The chart names
	// the Job + pods after the release (== the build CR name), so we
	// look up pods labelled with that instance name. We deliberately
	// don't try to resolve the env CR — there isn't one, this is the
	// kaniko Job pod that ran the build.
	if strings.HasPrefix(env, "build:") {
		buildName := strings.TrimPrefix(env, "build:")
		// First-look: if the build is already terminal, skip the
		// live-stream path entirely and ship the archived snapshot.
		// This avoids the "tail a Completed pod and close the WS
		// the moment it returns EOF" UX that surfaced as "connection
		// lost" in the deployments tab — kaniko's main container
		// exits on success, kubectl-logs follow returns immediately,
		// and the WS closes before the user sees the result.
		alreadyTerminal := false
		if s.Kube != nil && s.Kube.Clientset != nil {
			if jb, jerr := s.Kube.Clientset.BatchV1().Jobs(s.Namespace).Get(ctx, buildName, metav1.GetOptions{}); jerr == nil {
				if jb.Status.Succeeded > 0 || jb.Status.Failed > 0 {
					alreadyTerminal = true
				}
			}
		}
		if alreadyTerminal && s.BuildLogs != nil {
			if archived, err := s.BuildLogs.GetBuildLog(ctx, buildName); err == nil && archived != "" {
				for _, line := range strings.Split(archived, "\n") {
					if line == "" {
						continue
					}
					if err := sink.Write(Frame{Type: "log", Pod: buildName, Line: line}); err != nil {
						return env, nil
					}
				}
				_ = sink.Write(Frame{Type: "phase", Value: "completed"})
				return env, nil
			}
		}
		pods, err := s.Kube.Clientset.CoreV1().Pods(s.Namespace).List(ctx, metav1.ListOptions{
			LabelSelector: "app.kubernetes.io/instance=" + buildName,
		})
		if err != nil {
			return env, fmt.Errorf("list build pods: %w", err)
		}
		if len(pods.Items) == 0 {
			// Job pod's been GC'd by its TTL. Fall back to the archive
			// snapshot the build poller took at terminal-phase
			// transition (last 200 lines × init+main containers).
			if s.BuildLogs != nil {
				if archived, err := s.BuildLogs.GetBuildLog(ctx, buildName); err == nil && archived != "" {
					_ = sink.Write(Frame{Type: "log", Pod: buildName, Line: "── archived logs (pod GC'd) ──"})
					for _, line := range strings.Split(archived, "\n") {
						if line == "" {
							continue
						}
						if err := sink.Write(Frame{Type: "log", Pod: buildName, Line: line}); err != nil {
							return env, nil
						}
					}
					_ = sink.Write(Frame{Type: "phase", Value: "completed"})
					return env, nil
				}
			}
			// Distinguish "operator hasn't rendered yet" from "Job ran
			// and was GC'd". The KusoBuild CR exists either way, but a
			// brand-new build with no Job means the helm-operator hasn't
			// caught up to the watch event — a few seconds for the
			// reconcile to fire. We poll briefly so the deployments tab
			// transitions cleanly into live tail instead of flashing
			// "build pod not found" on every redeploy.
			waitDeadline := time.Now().Add(20 * time.Second)
			for time.Now().Before(waitDeadline) {
				select {
				case <-ctx.Done():
					return env, nil
				case <-time.After(2 * time.Second):
				}
				pods2, err := s.Kube.Clientset.CoreV1().Pods(s.Namespace).List(ctx, metav1.ListOptions{
					LabelSelector: "app.kubernetes.io/instance=" + buildName,
				})
				if err == nil && len(pods2.Items) > 0 {
					_ = sink.Write(Frame{Type: "phase", Value: "starting"})
					err = s.streamPods(ctx, pods2.Items, tailLines, sink)
					_ = sink.Write(Frame{Type: "phase", Value: "completed"})
					return env, err
				}
			}
			_ = sink.Write(Frame{Type: "log", Pod: buildName, Line: "build pod hasn't started yet — operator may be reconciling. Try again in a few seconds."})
			_ = sink.Write(Frame{Type: "phase", Value: "completed"})
			return env, nil
		}
		// Pod still alive. streamPods follows it; on EOF (pod
		// completes mid-stream), send a phase=completed frame so the
		// client closes the WS cleanly instead of showing
		// "connection lost".
		err = s.streamPods(ctx, pods.Items, tailLines, sink)
		_ = sink.Write(Frame{Type: "phase", Value: "completed"})
		return env, err
	}

	envName := env
	if !strings.Contains(env, "-") {
		envName = fqn + "-" + env
	}

	envCR, err := s.Kube.GetKusoEnvironment(ctx, s.Namespace, envName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return envName, ErrNotFound
		}
		return envName, fmt.Errorf("get env: %w", err)
	}
	// Tenancy check — env CR's spec is authoritative. Pre-v0.9.x the
	// `!= ""` form let zero-valued specs (legacy CRs / decode errors)
	// short-circuit the check and return logs from any env in the
	// namespace. Reject missing or mismatched fields outright.
	if envCR.Spec.Project != project || envCR.Spec.Service != fqn {
		return envName, ErrNotFound
	}

	pods, err := s.Kube.Clientset.CoreV1().Pods(s.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/instance=" + envName,
	})
	if err != nil {
		return envName, fmt.Errorf("list pods: %w", err)
	}
	if len(pods.Items) == 0 {
		// No pods yet — just keep the WS open so the client sees frames
		// the moment the first pod boots. We retry the listing on a slow
		// loop until ctx is done.
		return envName, s.streamWaitForPods(ctx, envName, tailLines, sink)
	}

	return envName, s.streamPods(ctx, pods.Items, tailLines, sink)
}

// streamWaitForPods polls every 3s for new pods. As soon as one shows
// up, it transitions into streamPods.
func (s *Service) streamWaitForPods(ctx context.Context, envName string, tailLines int, sink Sink) error {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-heartbeat.C:
			if err := sink.Write(Frame{Type: "ping"}); err != nil {
				return err
			}
		case <-t.C:
			pods, err := s.Kube.Clientset.CoreV1().Pods(s.Namespace).List(ctx, metav1.ListOptions{
				LabelSelector: "app.kubernetes.io/instance=" + envName,
			})
			if err != nil {
				continue
			}
			if len(pods.Items) > 0 {
				return s.streamPods(ctx, pods.Items, tailLines, sink)
			}
		}
	}
}

// streamPods spawns one goroutine per pod, fans into sink, returns when
// all goroutines finish or ctx is canceled.
func (s *Service) streamPods(ctx context.Context, pods []corev1.Pod, tailLines int, sink Sink) error {
	var wg sync.WaitGroup
	frames := make(chan Frame, 64)

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Aggregate cap. Counted at sink-write time below so a misbehaving
	// pod can't burn through the whole budget by itself — the reader
	// loop is the choke point, applying back-pressure naturally.
	var totalEmitted int

	for i := range pods {
		pod := pods[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.streamOnePod(streamCtx, pod, tailLines, frames)
		}()
	}

	// Heartbeat into the same fan-in channel so the handler doesn't need
	// a separate timer.
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-streamCtx.Done():
				return
			case <-t.C:
				select {
				case frames <- Frame{Type: "ping"}:
				case <-streamCtx.Done():
					return
				}
			}
		}
	}()

	// Closer: when every pod goroutine + heartbeat exits, close the
	// channel so the writer loop drains and returns.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	for {
		select {
		case f, ok := <-frames:
			if !ok {
				return nil
			}
			// Drop log frames once the aggregate cap is hit. The first
			// drop emits a one-shot notice frame so the user knows
			// further history was truncated; pings still flow so the
			// connection stays warm.
			if f.Type == "log" && totalEmitted >= maxAggregateTailFrames {
				if totalEmitted == maxAggregateTailFrames {
					_ = sink.Write(Frame{
						Type:    "error",
						Message: fmt.Sprintf("log buffer cap reached (%d frames); reduce tail or filter pods", maxAggregateTailFrames),
					})
					totalEmitted++
				}
				continue
			}
			if err := sink.Write(f); err != nil {
				cancel()
				return err
			}
			if f.Type == "log" {
				totalEmitted++
			}
		case <-done:
			// Drain remaining buffered frames before returning. The
			// aggregate cap still applies — a 50-pod env that flushed
			// late could otherwise emit a second burst here that
			// blew past the budget the main arm is enforcing.
			for {
				select {
				case f := <-frames:
					if f.Type == "log" && totalEmitted >= maxAggregateTailFrames {
						continue
					}
					if err := sink.Write(f); err != nil {
						return err
					}
					if f.Type == "log" {
						totalEmitted++
					}
				default:
					return nil
				}
			}
		case <-ctx.Done():
			cancel()
			return nil
		}
	}
}

// streamOnePod opens kube's follow stream and pumps lines onto frames.
// For build pods (label app.kubernetes.io/instance=<KusoBuild name>)
// it streams init containers FIRST (clone), then the main kaniko
// container, so the deployments tab shows the full lifecycle. Without
// this, a stuck clone container looked like "waiting for logs…"
// forever because we never streamed the init containers' output.
//
// Pod-phase transitions are emitted as separate frames so the UI can
// render "PodInitializing", "Running", "Succeeded" above the log
// pane while logs are still flowing.
func (s *Service) streamOnePod(ctx context.Context, pod corev1.Pod, tailLines int, frames chan<- Frame) {
	// Phase watcher: emits a "phase" frame on start + on each
	// transition. Independent of the log stream so users see the pod's
	// state evolve even while the kaniko container is still init'ing.
	go s.watchPodPhase(ctx, pod.Name, frames)

	// Stream init containers serially (clone first), then the main
	// container. Init container logs are bounded and short — we read
	// them to completion before falling through to the main one.
	for _, c := range pod.Spec.InitContainers {
		s.streamOneContainer(ctx, pod.Name, c.Name, true, tailLines, frames)
	}
	if len(pod.Spec.Containers) == 0 {
		return
	}
	primary := pod.Spec.Containers[0].Name
	s.streamOneContainer(ctx, pod.Name, primary, false, tailLines, frames)
}

// streamOneContainer opens a kube log follow stream for a single
// container in a pod. Init containers run to completion so we don't
// pass Follow=true; the main container we follow until ctx cancels
// or kaniko exits.
func (s *Service) streamOneContainer(ctx context.Context, podName, container string, isInit bool, tailLines int, frames chan<- Frame) {
	tail := int64(tailLines)
	opts := &corev1.PodLogOptions{
		Container: container,
		Follow:    !isInit,
		TailLines: &tail,
	}
	req := s.Kube.Clientset.CoreV1().Pods(s.Namespace).GetLogs(podName, opts)
	stream, err := req.Stream(ctx)
	if err != nil {
		// "container is waiting" / "container ... not found" is the
		// normal case for an init container that hasn't started yet
		// or a main container we're racing. Don't surface as error;
		// the phase watcher will tell the user what's going on.
		select {
		case frames <- Frame{Type: "log", Pod: podName, Stream: "stderr", Line: "── " + container + ": " + err.Error() + " ──"}:
		case <-ctx.Done():
		}
		return
	}
	defer stream.Close()

	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		text := scanner.Text()
		if text == "" {
			continue
		}
		// Tag init-container lines with the container name so the UI
		// can render a subtle prefix ("clone | ..."). Main container
		// logs stay un-prefixed.
		line := text
		if isInit {
			line = container + " | " + text
		}
		select {
		case frames <- Frame{
			Type:   "log",
			Pod:    podName,
			Line:   line,
			Stream: "stdout",
			Ts:     time.Now().UTC().Format(time.RFC3339),
		}:
		case <-ctx.Done():
			return
		}
	}
}

// watchPodPhase polls the pod's status and emits "phase" frames on
// transitions. We start by emitting the current phase so the UI has
// something to show before the first log line; then re-poll every 2s
// and emit on change. Stops when the pod reaches a terminal phase
// (Succeeded/Failed) or ctx cancels.
func (s *Service) watchPodPhase(ctx context.Context, podName string, frames chan<- Frame) {
	last := ""
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		pod, err := s.Kube.Clientset.CoreV1().Pods(s.Namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			return
		}
		phase := derivePodPhase(pod)
		if phase != last {
			select {
			case frames <- Frame{Type: "phase", Pod: podName, Value: phase}:
			case <-ctx.Done():
				return
			}
			last = phase
		}
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// derivePodPhase returns a more user-friendly status string than
// pod.Status.Phase. The raw phase ("Pending") doesn't tell the user
// whether they're waiting on the scheduler, image pull, or an init
// container; we surface the most-informative containerStatus reason
// when present.
func derivePodPhase(pod *corev1.Pod) string {
	// Init-container progress: the first one that's still running or
	// waiting wins (that's what's blocking the main container).
	for _, cs := range pod.Status.InitContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
			return "init: " + cs.Name + " (" + cs.State.Waiting.Reason + ")"
		}
		if cs.State.Running != nil {
			return "init: " + cs.Name + " (running)"
		}
	}
	// Main containers: same waiting-reason / running-or-terminated dance.
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
			return cs.State.Waiting.Reason
		}
		if cs.State.Running != nil {
			return "Running"
		}
		if cs.State.Terminated != nil {
			if cs.State.Terminated.Reason != "" {
				return cs.State.Terminated.Reason
			}
			return string(pod.Status.Phase)
		}
	}
	if string(pod.Status.Phase) != "" {
		return string(pod.Status.Phase)
	}
	return "Pending"
}
