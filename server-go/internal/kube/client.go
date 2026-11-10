// Package kube wraps client-go for the kuso server. It exposes a single
// Client struct that holds a typed clientset (for core resources like
// Secret, Pod, ConfigMap) and a dynamic.Interface (for our six CRDs).
//
// CRD typing is hand-rolled in types.go + crds.go rather than codegen.
package kube

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/flowcontrol"
)

// Client bundles the kube API surfaces the server uses.
//
// Constructed by NewClient; the typed *kubernetes.Clientset is for core
// resources (Secret, Pod, Namespace, Event), the dynamic.Interface is for
// our CRDs in application.kuso.sislelabs.com/v1alpha1.
type Client struct {
	Clientset kubernetes.Interface
	Dynamic   dynamic.Interface
	Config    *rest.Config

	// LeaseClientset is a clientset with its own client-side rate
	// limiter, used only for leader-election lease renewals. Sharing the
	// main limiter let a 1,000-env propagation fan-out queue a renewal
	// past RenewDeadline, dropping the lease and re-running every
	// one-shot on re-acquire. Nil in tests; use LeaseClient().
	LeaseClientset kubernetes.Interface

	// envWritePacer paces env-CR read-modify-writes so a project-wide
	// fan-out (addon add, shared-key change across ~1,000 envs) can't
	// take the whole client QPS budget from interactive requests. Burst
	// covers a normal project without any delay. Nil = unpaced (tests).
	envWritePacer flowcontrol.RateLimiter

	// Cache is the optional shared informer over kuso CRDs. When set,
	// list[T] reads from the local cache instead of LIST'ing the API
	// server. Constructed and started by the server bootstrap; nil
	// in tests and CLI flows where a single round-trip is fine.
	Cache *Cache
}

// NewClient resolves a *rest.Config and builds typed + dynamic clients.
//
// Resolution order:
//  1. KUBECONFIG env var (colon-separated paths; clientcmd merges them).
//  2. ~/.kube/config (if HOME is set).
//  3. In-cluster config (ServiceAccount token at /var/run/secrets/...).
//
// We try out-of-cluster first because dev runs hit a local kubeconfig and
// in-cluster pods don't have $HOME or KUBECONFIG set, so the fallback to
// rest.InClusterConfig() lands cleanly.
func NewClient() (*Client, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, fmt.Errorf("kube: load config: %w", err)
	}
	return newClientFromConfig(cfg)
}

// NewClientFromKubeconfig is the explicit-path variant — useful in tests
// and CLI-driven flows that pass --kubeconfig.
func NewClientFromKubeconfig(path string) (*Client, error) {
	cfg, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		return nil, fmt.Errorf("kube: build config from %q: %w", path, err)
	}
	return newClientFromConfig(cfg)
}

func newClientFromConfig(cfg *rest.Config) (*Client, error) {
	// Bump default QPS/burst. client-go ships with 5 QPS / 10 burst,
	// fine for a CLI but way too tight for a long-running control-
	// plane that fans out spec writes across N envs in tight loops
	// (propagation, migration, addon-conn refresh). Real-world failure
	// mode: SetSharedEnvKeys triggers propagateChangedToEnvs → per-env
	// secret reads → rate-limit Wait > context deadline → propagation
	// silently no-ops and the env CR is never updated. 50 QPS / 100
	// burst leaves plenty of headroom without flooding the apiserver
	// (k3s default qps-limit is 200; we sit well under that).
	//
	// Raised to 100/200 alongside the env-write pacer below: fan-outs are
	// now held to ~60% of the budget, and server-side APF protects the
	// apiserver itself.
	if cfg.QPS == 0 {
		cfg.QPS = 100
		cfg.Burst = 200
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("kube: typed clientset: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("kube: dynamic client: %w", err)
	}
	lcfg := rest.CopyConfig(cfg)
	lcfg.RateLimiter = nil
	lcfg.QPS = 10
	lcfg.Burst = 20
	lcs, err := kubernetes.NewForConfig(lcfg)
	if err != nil {
		return nil, fmt.Errorf("kube: lease clientset: %w", err)
	}
	return &Client{
		Clientset: cs, Dynamic: dyn, Config: cfg, LeaseClientset: lcs,
		envWritePacer: flowcontrol.NewTokenBucketRateLimiter(envWriteQPS, envWriteBurst),
	}, nil
}

// Each paced env write is a GET + UPDATE, so 30/s is ~60 of the 100
// QPS budget at worst, leaving the rest for UI and loops.
const (
	envWriteQPS   = 30
	envWriteBurst = 60
)

// paceEnvWrite blocks until the env-write pacer admits one more write.
func (c *Client) paceEnvWrite(ctx context.Context) error {
	if c == nil || c.envWritePacer == nil {
		return nil
	}
	if err := c.envWritePacer.Wait(ctx); err != nil {
		return fmt.Errorf("kube: env write pacer: %w", err)
	}
	return nil
}

// LeaseClient returns the clientset leader election should use: the
// private-limiter one when built, else the shared Clientset.
func (c *Client) LeaseClient() kubernetes.Interface {
	if c.LeaseClientset != nil {
		return c.LeaseClientset
	}
	return c.Clientset
}

// EnableCache attaches a shared informer cache to the client and
// starts the watches. Long-lived processes (the kuso server) should
// call this once on boot; one-shot CLI invocations should not.
//
// Safe to call multiple times — the second call is a no-op.
func (c *Client) EnableCache() {
	if c == nil || c.Cache != nil {
		return
	}
	c.Cache = NewCache(c)
	c.Cache.Start()
}

func loadConfig() (*rest.Config, error) {
	if path := os.Getenv("KUBECONFIG"); path != "" {
		return clientcmd.BuildConfigFromFlags("", path)
	}
	if home, err := os.UserHomeDir(); err == nil {
		def := filepath.Join(home, ".kube", "config")
		if _, statErr := os.Stat(def); statErr == nil {
			return clientcmd.BuildConfigFromFlags("", def)
		}
	}
	return rest.InClusterConfig()
}
