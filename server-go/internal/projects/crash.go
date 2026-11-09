package projects

import (
	"bufio"
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/failures"
	"kuso/server/internal/linescan"
)

// CrashReport is the wire shape for GET .../services/{s}/crash: the last
// time one of the env's containers died, what it printed before dying,
// and the classifier's reading of that output. It answers "why does it
// keep crashing" without leaving the deployments view.
type CrashReport struct {
	Pod       string `json:"pod"`
	Container string `json:"container"`
	Restarts  int32  `json:"restarts"`
	// Reason/ExitCode/FinishedAt come from the container's last
	// termination (Error, OOMKilled, Completed …).
	Reason     string `json:"reason,omitempty"`
	ExitCode   int32  `json:"exitCode"`
	FinishedAt string `json:"finishedAt,omitempty"`
	// Log is the tail of the run that died, oldest first.
	Log            []string                 `json:"log"`
	Classification *failures.Classification `json:"classification,omitempty"`
}

const (
	crashClassifyLines = 50
	crashShownLines    = 12
)

// crashTarget is the container whose last run is the one to explain.
type crashTarget struct {
	pod       *corev1.Pod
	container string
	restarts  int32
	term      *corev1.ContainerStateTerminated
	// previous: the termination belongs to an earlier container (the
	// kubelet already restarted it), so its logs are the Previous ones.
	previous bool
}

// pickCrash chooses the most recent termination among the env's pods,
// preferring pods of the current ReplicaSet (a draining old pod's exit
// isn't the failure being asked about) and non-zero exits.
func pickCrash(pods []corev1.Pod, currentRS string) *crashTarget {
	var best *crashTarget
	score := func(t *crashTarget) (int, time.Time) {
		s := 0
		if podIsCurrent(t.pod, currentRS) {
			s += 2
		}
		if t.term.ExitCode != 0 {
			s++
		}
		return s, t.term.FinishedAt.Time
	}
	for i := range pods {
		p := &pods[i]
		if p.DeletionTimestamp != nil {
			continue
		}
		if owner := metav1.GetControllerOf(p); owner != nil && owner.Kind == "Job" {
			continue
		}
		for _, cs := range p.Status.ContainerStatuses {
			var t *crashTarget
			switch {
			case cs.State.Terminated != nil:
				t = &crashTarget{pod: p, container: cs.Name, restarts: cs.RestartCount, term: cs.State.Terminated}
			case cs.LastTerminationState.Terminated != nil && cs.RestartCount > 0:
				t = &crashTarget{pod: p, container: cs.Name, restarts: cs.RestartCount, term: cs.LastTerminationState.Terminated, previous: true}
			default:
				continue
			}
			if best == nil {
				best = t
				continue
			}
			bs, bt := score(best)
			ts, tt := score(t)
			if ts > bs || (ts == bs && tt.After(bt)) {
				best = t
			}
		}
	}
	return best
}

// CrashReport reads the env's latest container termination. nil, nil when
// no container of the env has died (nothing to explain).
func (s *Service) CrashReport(ctx context.Context, project, service, env string) (*CrashReport, error) {
	ns, envCR, err := s.resolveOwnedEnv(ctx, project, service, env)
	if err != nil {
		return nil, err
	}
	pods, err := s.Kube.Clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/instance=" + envCR.Name,
	})
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	t := pickCrash(pods.Items, s.currentReplicaSet(ctx, ns, envCR.Name))
	if t == nil {
		return nil, nil
	}
	lines := s.containerLogTail(ctx, ns, t.pod.Name, t.container, t.previous, crashClassifyLines)
	cls := failures.Classify(lines, failures.Signal{Reason: t.term.Reason, ExitCode: t.term.ExitCode, Runtime: true})
	shown := lines
	if len(shown) > crashShownLines {
		shown = shown[len(shown)-crashShownLines:]
	}
	out := &CrashReport{
		Pod:       t.pod.Name,
		Container: t.container,
		Restarts:  t.restarts,
		Reason:    t.term.Reason,
		ExitCode:  t.term.ExitCode,
		Log:       shown,
	}
	if !t.term.FinishedAt.IsZero() {
		out.FinishedAt = t.term.FinishedAt.UTC().Format(time.RFC3339)
	}
	if out.Log == nil {
		out.Log = []string{}
	}
	if cls.Kind != failures.KindGeneric {
		out.Classification = &cls
	}
	return out, nil
}

// containerLogTail reads the last n lines of one container's run.
// Best-effort: a missing previous container or a log read error gives
// an empty tail rather than failing the report.
func (s *Service) containerLogTail(ctx context.Context, ns, pod, container string, previous bool, n int64) []string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	stream, err := s.Kube.Clientset.CoreV1().Pods(ns).GetLogs(pod, &corev1.PodLogOptions{
		Container: container,
		Previous:  previous,
		TailLines: &n,
	}).Stream(ctx)
	if err != nil {
		return nil
	}
	defer stream.Close()
	var out []string
	sc := bufio.NewScanner(stream)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	sc.Split(linescan.Lines(linescan.DefaultMaxLine))
	for sc.Scan() {
		if l := sc.Text(); l != "" {
			out = append(out, l)
		}
	}
	return out
}
