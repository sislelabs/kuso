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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1 "k8s.io/api/core/v1"

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
	Kube      *kube.Client
	Namespace string
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
	envName := env
	if !strings.Contains(env, "-") {
		envName = fqn + "-" + env
	}

	// Verify the env exists — saves the caller a confusing empty result
	// when they typo a name.
	if _, err := s.Kube.GetKusoEnvironment(ctx, s.Namespace, envName); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, envName, ErrNotFound
		}
		return nil, envName, fmt.Errorf("get env: %w", err)
	}

	pods, err := s.Kube.Clientset.CoreV1().Pods(s.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/instance=" + envName,
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
		podLines, err := s.tailOnePod(ctx, pod, perPod)
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
func (s *Service) tailOnePod(ctx context.Context, pod *corev1.Pod, tailLines int64) ([]Line, error) {
	req := s.Kube.Clientset.CoreV1().Pods(s.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
		TailLines: &tailLines,
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		return nil, err
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
	if err := scanner.Err(); err != nil {
		return out, err
	}
	return out, nil
}
