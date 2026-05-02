// Package status provides version readers used by /api/auth/session and
// /api/status to surface kuso UI version, kube version, and operator
// image tag.
//
// Versions are cached and refreshed lazily — they change rarely and the
// session route is high-traffic.
package status

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// Service exposes version readers cached for ~5 minutes.
type Service struct {
	Kube *kube.Client

	mu        sync.RWMutex
	kubeVer   string
	opVer     string
	loadedAt  time.Time
	staleness time.Duration
}

// New constructs a Service. staleness=0 falls back to 5 minutes.
func New(k *kube.Client, staleness time.Duration) *Service {
	if staleness <= 0 {
		staleness = 5 * time.Minute
	}
	return &Service{Kube: k, staleness: staleness}
}

// Versions returns the cached pair, refreshing if older than staleness.
// Errors are swallowed — versions are best-effort cosmetic data.
func (s *Service) Versions(ctx context.Context) (kubeVersion, operatorVersion string) {
	s.mu.RLock()
	if time.Since(s.loadedAt) < s.staleness && s.loadedAt != (time.Time{}) {
		k, o := s.kubeVer, s.opVer
		s.mu.RUnlock()
		return k, o
	}
	s.mu.RUnlock()

	s.refresh(ctx)
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.kubeVer, s.opVer
}

func (s *Service) refresh(ctx context.Context) {
	kv := s.fetchKubeVersion(ctx)
	ov := s.fetchOperatorVersion(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	if kv != "" {
		s.kubeVer = kv
	}
	if ov != "" {
		s.opVer = ov
	}
	s.loadedAt = time.Now()
}

func (s *Service) fetchKubeVersion(_ context.Context) string {
	if s.Kube == nil || s.Kube.Clientset == nil {
		return ""
	}
	v, err := s.Kube.Clientset.Discovery().ServerVersion()
	if err != nil || v == nil {
		return "unknown"
	}
	return v.GitVersion
}

func (s *Service) fetchOperatorVersion(ctx context.Context) string {
	if s.Kube == nil || s.Kube.Clientset == nil {
		return ""
	}
	pods, err := s.Kube.Clientset.CoreV1().Pods("kuso-operator-system").List(ctx, metav1.ListOptions{})
	if err != nil {
		return ""
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		if !strings.HasPrefix(p.Name, "kuso-operator-controller-manager") {
			continue
		}
		for _, c := range p.Spec.Containers {
			if !strings.HasPrefix(c.Name, "manager") {
				continue
			}
			// Image is "ghcr.io/.../kuso-operator:vX.Y.Z" — split on
			// the last colon for the tag.
			if i := strings.LastIndex(c.Image, ":"); i > 0 && i < len(c.Image)-1 {
				return c.Image[i+1:]
			}
			return c.Image
		}
	}
	return ""
}

// HealthzBody is the shape /healthz returns. Defined here so the router
// shares it with /api/status.
type HealthzBody struct {
	Status   string `json:"status"`
	Version  string `json:"version"`
	Kube     string `json:"kubernetesVersion,omitempty"`
	Operator string `json:"operatorVersion,omitempty"`
}

// Health returns a populated HealthzBody including version metadata.
func (s *Service) Health(ctx context.Context, kusoVersion string) HealthzBody {
	kv, ov := s.Versions(ctx)
	return HealthzBody{Status: "ok", Version: kusoVersion, Kube: kv, Operator: ov}
}

// String returns a human one-liner for log lines / smoke output.
func (s *Service) String() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return fmt.Sprintf("kube=%s operator=%s loaded=%s", s.kubeVer, s.opVer, s.loadedAt.Format(time.RFC3339))
}
