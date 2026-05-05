// Package kube wraps client-go for the kuso server. It exposes a single
// Client struct that holds a typed clientset (for core resources like
// Secret, Pod, ConfigMap) and a dynamic.Interface (for our six CRDs).
//
// CRD typing is hand-rolled in types.go + crds.go rather than codegen.
// See kuso/docs/REWRITE.md §3 ("no codegen unless we hit pain").
package kube

import (
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
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
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("kube: typed clientset: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("kube: dynamic client: %w", err)
	}
	return &Client{Clientset: cs, Dynamic: dyn, Config: cfg}, nil
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
