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
func (s *Service) Stream(ctx context.Context, project, service, env string, tailLines int, sink Sink) (string, error) {
	if tailLines <= 0 {
		tailLines = 100
	}
	if tailLines > 5000 {
		tailLines = 5000
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
		pods, err := s.Kube.Clientset.CoreV1().Pods(s.Namespace).List(ctx, metav1.ListOptions{
			LabelSelector: "app.kubernetes.io/instance=" + buildName,
		})
		if err != nil {
			return env, fmt.Errorf("list build pods: %w", err)
		}
		if len(pods.Items) == 0 {
			// Job pods get GC'd after success/failure. Return a clean
			// info frame and let the WS close naturally instead of
			// looping forever waiting for a pod that won't exist.
			_ = sink.Write(Frame{Type: "log", Pod: buildName, Line: "build pod not found (likely garbage-collected)"})
			return env, nil
		}
		return env, s.streamPods(ctx, pods.Items, tailLines, sink)
	}

	envName := env
	if !strings.Contains(env, "-") {
		envName = fqn + "-" + env
	}

	if _, err := s.Kube.GetKusoEnvironment(ctx, s.Namespace, envName); err != nil {
		if apierrors.IsNotFound(err) {
			return envName, ErrNotFound
		}
		return envName, fmt.Errorf("get env: %w", err)
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
			if err := sink.Write(f); err != nil {
				cancel()
				return err
			}
		case <-done:
			// Drain remaining buffered frames before returning.
			for {
				select {
				case f := <-frames:
					if err := sink.Write(f); err != nil {
						return err
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
func (s *Service) streamOnePod(ctx context.Context, pod corev1.Pod, tailLines int, frames chan<- Frame) {
	tail := int64(tailLines)
	req := s.Kube.Clientset.CoreV1().Pods(s.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
		Follow:    true,
		TailLines: &tail,
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		select {
		case frames <- Frame{Type: "error", Pod: pod.Name, Message: err.Error()}:
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
		select {
		case frames <- Frame{
			Type:   "log",
			Pod:    pod.Name,
			Line:   text,
			Stream: "stdout",
			Ts:     time.Now().UTC().Format(time.RFC3339),
		}:
		case <-ctx.Done():
			return
		}
	}
}
