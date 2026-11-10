package builds

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"
)

// RegistryAuthFunc returns the Authorization header for the in-cluster
// registry, ok=false while no credential exists.
type RegistryAuthFunc func(ctx context.Context) (header string, ok bool)

var registryAuth atomic.Pointer[RegistryAuthFunc]

// SetRegistryAuth wires the kuso-registry credential source (main.go,
// once). Every in-cluster registry client built by this package, and any
// client from RegistryHTTPClient, sends it. The registry ignores the
// header while auth is off, so sending it early is what lets auth be
// switched on later without a window where the sweep or build reuse 401s.
func SetRegistryAuth(f RegistryAuthFunc) {
	if f == nil {
		registryAuth.Store(nil)
		return
	}
	registryAuth.Store(&f)
}

type registryAuthTransport struct{ base http.RoundTripper }

func (t registryAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if f := registryAuth.Load(); f != nil && req.URL.Host == RegistryHost && req.Header.Get("Authorization") == "" {
		if h, ok := (*f)(req.Context()); ok {
			req = req.Clone(req.Context())
			req.Header.Set("Authorization", h)
		}
	}
	return t.base.RoundTrip(req)
}

// RegistryHTTPClient is an http.Client that authenticates to the
// in-cluster registry when a credential is configured.
func RegistryHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: registryAuthTransport{base: http.DefaultTransport}}
}
