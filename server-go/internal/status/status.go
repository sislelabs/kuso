// Package status provides version readers used by /api/auth/session and
// /api/status to surface kuso UI version, kube version, and operator
// image tag.
//
// Versions are cached and refreshed lazily — they change rarely and the
// session route is high-traffic.
package status

import (
	"context"
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
	refreshMu sync.Mutex
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
// Errors are swallowed — versions are best-effort cosmetic data. One caller
// refreshes at a time; the others get the stale pair meanwhile (or wait, on
// the very first load).
func (s *Service) Versions(ctx context.Context) (kubeVersion, operatorVersion string) {
	k, o, fresh, loaded := s.cached()
	if fresh {
		return k, o
	}
	if !s.refreshMu.TryLock() {
		if loaded {
			return k, o
		}
		s.refreshMu.Lock()
	}
	defer s.refreshMu.Unlock()
	if k, o, fresh, _ = s.cached(); fresh {
		return k, o
	}
	s.refresh(ctx)
	k, o, _, _ = s.cached()
	return k, o
}

func (s *Service) cached() (kubeVer, opVer string, fresh, loaded bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	loaded = !s.loadedAt.IsZero()
	return s.kubeVer, s.opVer, loaded && time.Since(s.loadedAt) < s.staleness, loaded
}

func (s *Service) refresh(ctx context.Context) {
	kv, kerr := s.fetchKubeVersion(ctx)
	ov := s.fetchOperatorVersion(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case kv != "":
		s.kubeVer = kv
	case kerr && s.kubeVer == "":
		s.kubeVer = "unknown"
	}
	if ov != "" {
		s.opVer = ov
	}
	s.loadedAt = time.Now()
}

// fetchKubeVersion reports failed=true on a discovery error so a blip keeps
// the cached version instead of overwriting it with "unknown".
func (s *Service) fetchKubeVersion(_ context.Context) (version string, failed bool) {
	if s.Kube == nil || s.Kube.Clientset == nil {
		return "", false
	}
	v, err := s.Kube.Clientset.Discovery().ServerVersion()
	if err != nil || v == nil {
		return "", true
	}
	return v.GitVersion, false
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
			// Image is "ghcr.io/.../kuso-operator:vX.Y.Z", optionally
			// digest-pinned ("…:vX.Y.Z@sha256:…") — drop the digest,
			// then split on the last colon for the tag.
			img, _, _ := strings.Cut(c.Image, "@")
			if i := strings.LastIndex(img, ":"); i > 0 && i < len(img)-1 {
				return img[i+1:]
			}
			return img
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
