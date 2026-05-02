// Package config wraps the KusoConfig CR (CRD `Kuso`, plural `kusoes`)
// and the runpacks/podsizes seeded into the SQLite DB on first boot.
//
// Two surfaces:
//   - Settings: read/write the entire spec of the singleton Kuso CR
//     (admin /api/config endpoints).
//   - Feature flags: env-var-driven toggles the auth + UI surface reads
//     (localauth/github/oauth2 enabled, sleep, metrics, console, etc.).
//     Some flags also depend on the CR contents — those load on boot.
package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"kuso/server/internal/kube"
)

// Service holds the in-memory mirror of the Kuso CR plus boolean flags
// that change rarely. ReloadInterval=0 means manual-only reload (call
// Reload from a hook); >0 means the Run goroutine refreshes the cache
// on that cadence.
type Service struct {
	Kube      *kube.Client
	Namespace string

	mu       sync.RWMutex
	settings map[string]any // last successful read of Kuso.spec
	features Features
}

// Features tracks toggles whose value changes rarely. The auth/session
// surface reads these on every request, so they're kept in memory.
type Features struct {
	Sleep            bool
	Metrics          bool
	BuildPipeline    bool
	TemplatesEnabled bool
	ConsoleEnabled   bool
	AuditEnabled     bool
	AdminDisabled    bool
	LocalAuth        bool
	GithubAuth       bool
	OAuth2Auth       bool
}

// New constructs a Service. namespace falls back to "kuso".
func New(k *kube.Client, namespace string) *Service {
	if namespace == "" {
		namespace = "kuso"
	}
	return &Service{Kube: k, Namespace: namespace, features: featuresFromEnv()}
}

// featuresFromEnv reads the env-driven boolean flags. The Kuso-CR-driven
// fields (sleep, banner, console.enabled, admin.disabled) are folded in
// by Reload from the live CR.
func featuresFromEnv() Features {
	return Features{
		BuildPipeline:    os.Getenv("KUSO_BUILD_REGISTRY") != "",
		TemplatesEnabled: os.Getenv("KUSO_TEMPLATES_ENABLED") == "true",
		ConsoleEnabled:   os.Getenv("KUSO_CONSOLE_ENABLED") == "true",
		AuditEnabled:     os.Getenv("KUSO_AUDIT_ENABLED") != "false",
		AdminDisabled:    os.Getenv("KUSO_ADMIN_DISABLED") == "true",
		LocalAuth:        strings.TrimSpace(os.Getenv("KUSO_SESSION_KEY")) != "",
		GithubAuth: os.Getenv("GITHUB_CLIENT_SECRET") != "" &&
			os.Getenv("GITHUB_CLIENT_ID") != "" &&
			os.Getenv("GITHUB_CLIENT_CALLBACKURL") != "" &&
			os.Getenv("GITHUB_CLIENT_ORG") != "",
		OAuth2Auth: os.Getenv("OAUTH2_CLIENT_AUTH_URL") != "" &&
			os.Getenv("OAUTH2_CLIENT_TOKEN_URL") != "" &&
			os.Getenv("OAUTH2_CLIENT_ID") != "" &&
			os.Getenv("OAUTH2_CLIENT_SECRET") != "" &&
			os.Getenv("OAUTH2_CLIENT_CALLBACKURL") != "",
	}
}

// Features returns a copy of the current flag set.
func (s *Service) Features() Features {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.features
}

// Settings returns a copy of the cached Kuso.spec map.
func (s *Service) Settings() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]any, len(s.settings))
	for k, v := range s.settings {
		out[k] = v
	}
	return out
}

// Reload refreshes the cached Kuso CR + zeropod check. Safe to call
// concurrently — guarded by the same lock the readers take.
func (s *Service) Reload(ctx context.Context) error {
	if s.Kube == nil {
		return nil
	}
	spec := map[string]any{}
	kusoes, err := s.Kube.ListKusoes(ctx, s.Namespace)
	if err != nil {
		return fmt.Errorf("config: list kusoes: %w", err)
	}
	if len(kusoes) > 0 {
		spec = kusoes[0].Spec
	}

	sleep := false
	// zeropod-system namespace presence is the cheap heuristic the TS
	// server uses for "sleep mode is supported".
	if s.Kube.Clientset != nil {
		_, nsErr := s.Kube.Clientset.CoreV1().Namespaces().Get(ctx, "zeropod-system", metav1Get())
		sleep = nsErr == nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings = spec
	s.features.Sleep = sleep
	// CR-driven feature flags overlay the env-driven base.
	if kusoMap, ok := spec["kuso"].(map[string]any); ok {
		if console, ok := kusoMap["console"].(map[string]any); ok {
			if v, ok := console["enabled"].(bool); ok {
				s.features.ConsoleEnabled = v
			}
		}
		if admin, ok := kusoMap["admin"].(map[string]any); ok {
			if v, ok := admin["disabled"].(bool); ok {
				s.features.AdminDisabled = v
			}
		}
	}
	return nil
}

// Run reloads on Interval until ctx is cancelled. Best-effort — errors
// are surfaced via the logger but don't stop the loop.
func (s *Service) Run(ctx context.Context, interval time.Duration, onErr func(error)) {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	if err := s.Reload(ctx); err != nil && onErr != nil {
		onErr(err)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Reload(ctx); err != nil && onErr != nil {
				onErr(err)
			}
		}
	}
}

// UpdateSettings replaces the Kuso CR's spec with the provided map and
// refreshes the cache. Refuses when admin is disabled.
func (s *Service) UpdateSettings(ctx context.Context, spec map[string]any) error {
	if s.Features().AdminDisabled {
		return ErrAdminDisabled
	}
	kusoes, err := s.Kube.ListKusoes(ctx, s.Namespace)
	if err != nil {
		return fmt.Errorf("list kusoes: %w", err)
	}
	if len(kusoes) == 0 {
		return ErrNotFound
	}
	cr := &kusoes[0]
	cr.Spec = spec
	if _, err := s.Kube.Dynamic.Resource(kube.GVRKuso).Namespace(s.Namespace).
		Update(ctx, toUnstructuredKuso(cr), metav1Update()); err != nil {
		if apierrors.IsNotFound(err) {
			return ErrNotFound
		}
		return fmt.Errorf("update kuso: %w", err)
	}
	return s.Reload(ctx)
}

// Errors mirroring sibling packages.
var (
	ErrAdminDisabled = errors.New("config: admin disabled")
	ErrNotFound      = errors.New("config: not found")
)
