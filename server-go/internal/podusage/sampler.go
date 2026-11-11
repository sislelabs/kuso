package podusage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"kuso/server/internal/kube"
)

// Env pods carry the kusoenvironment chart's labels; instance is the
// KusoEnvironment name. Build, cron and release-hook pods don't match.
const (
	labelName     = "app.kubernetes.io/name"
	labelInstance = "app.kubernetes.io/instance"
	envChartName  = "kusoenvironment"
)

// Sampler feeds Store from the kubelets. It is not registered as a
// serverstate liveness loop: it runs on every replica, not under the
// leader lease, and a liveness entry there would gate non-leader probes.
type Sampler struct {
	Kube   *kube.Client
	Store  *Store
	Logger *slog.Logger

	failing bool
}

// Run blocks until ctx is cancelled, sampling once immediately.
func (s *Sampler) Run(ctx context.Context) {
	if s == nil || s.Kube == nil || s.Store == nil {
		return
	}
	if s.Logger == nil {
		s.Logger = slog.Default()
	}
	s.tick(ctx)
	t := time.NewTicker(SampleInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.tick(ctx)
		}
	}
}

func (s *Sampler) tick(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	err := s.sampleOnce(ctx)
	// A missing RBAC grant or a dead kubelet fails every tick; log the
	// transition, not each 30s repeat.
	switch {
	case err != nil && !s.failing:
		s.Logger.Warn("podusage: sample failed; history has a gap until it recovers", "err", err)
	case err == nil && s.failing:
		s.Logger.Info("podusage: sampling recovered")
	}
	s.failing = err != nil
}

func (s *Sampler) sampleOnce(ctx context.Context) error {
	nodes, err := s.nodes(ctx)
	if err != nil {
		return err
	}
	pods, err := s.envPods(ctx)
	if err != nil {
		return err
	}
	nodes = nodesHosting(nodes, pods)
	usage, errs := s.Kube.NodesPodUsage(ctx, nodes)
	if len(usage) == 0 && len(errs) > 0 {
		return fmt.Errorf("kubelet stats: %w", errors.Join(errs...))
	}
	s.Store.Record(time.Now(), Aggregate(pods, usage))
	return nil
}

// nodesHosting keeps the nodes that run at least one env pod. This
// sampler runs on every replica, and each kubelet summary is 0.5-1 MB
// through the apiserver proxy: nodes with only addons, builds or the
// control plane have nothing to contribute (SCL-10).
func nodesHosting(nodes []*corev1.Node, pods []*corev1.Pod) []*corev1.Node {
	used := make(map[string]struct{}, len(nodes))
	for _, p := range pods {
		if p.Spec.NodeName != "" {
			used[p.Spec.NodeName] = struct{}{}
		}
	}
	out := make([]*corev1.Node, 0, len(used))
	for _, n := range nodes {
		if _, ok := used[n.Name]; ok {
			out = append(out, n)
		}
	}
	return out
}

func (s *Sampler) nodes(ctx context.Context) ([]*corev1.Node, error) {
	if nodes, ok := s.Kube.Cache.ListNodes(); ok {
		return nodes, nil
	}
	if s.Kube.Clientset == nil {
		return nil, errors.New("kube clientset not wired")
	}
	raw, err := s.Kube.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	out := make([]*corev1.Node, len(raw.Items))
	for i := range raw.Items {
		out[i] = &raw.Items[i]
	}
	return out, nil
}

func (s *Sampler) envPods(ctx context.Context) ([]*corev1.Pod, error) {
	sel := labels.SelectorFromSet(labels.Set{labelName: envChartName})
	if pods, ok := s.Kube.Cache.ListPodsByLabel(sel); ok {
		return pods, nil
	}
	if s.Kube.Clientset == nil {
		return nil, errors.New("kube clientset not wired")
	}
	raw, err := s.Kube.Clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{LabelSelector: sel.String()})
	if err != nil {
		return nil, fmt.Errorf("list env pods: %w", err)
	}
	out := make([]*corev1.Pod, len(raw.Items))
	for i := range raw.Items {
		out[i] = &raw.Items[i]
	}
	return out, nil
}

// Aggregate sums per-pod kubelet usage into one Sample per service env.
// usage is keyed by node name; a node missing from it failed this tick,
// and an env with a running pod there is dropped rather than recorded
// as a partial (too low) total. Pods the kubelet has no stats for yet
// (just started) are left out of the sum and the pod count.
func Aggregate(pods []*corev1.Pod, usage map[string][]kube.PodUsage) map[Key]Sample {
	type podRef struct{ ns, name string }
	byPod := map[podRef]kube.PodUsage{}
	for _, list := range usage {
		for _, u := range list {
			byPod[podRef{u.Namespace, u.Name}] = u
		}
	}
	out := map[Key]Sample{}
	partial := map[Key]bool{}
	for _, p := range pods {
		if p.Status.Phase != corev1.PodRunning {
			continue
		}
		project := p.Labels[kube.LabelProject]
		service := p.Labels[kube.LabelService]
		env := p.Labels[labelInstance]
		if project == "" || service == "" || env == "" {
			continue
		}
		k := Key{Project: project, Service: strings.TrimPrefix(service, project+"-"), Env: env}
		if _, ok := usage[p.Spec.NodeName]; !ok {
			partial[k] = true
			continue
		}
		u, ok := byPod[podRef{p.Namespace, p.Name}]
		if !ok || u.MemWorkingSetBytes == 0 {
			continue
		}
		v := out[k]
		v.CPUMilli += int32((u.CPUNanoCores + 500_000) / 1_000_000)
		v.MemBytes += int64(u.MemWorkingSetBytes)
		v.Pods++
		out[k] = v
	}
	for k := range partial {
		delete(out, k)
	}
	return out
}
