package activator

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Serve-stale copies are per replica, and traefik reuses its keep-alive
// connections, so one replica can see every request for a site while the
// visitor who hits a sleeping app lands on the other one. Each replica
// therefore pushes what it captures to its peers over a listener that is
// not part of the kuso-activator Service, so no ingress route reaches it.

const (
	stalePeerPath     = "/__kuso/stale"
	stalePeerMetaHdr  = "X-Kuso-Stale-Meta"
	stalePeerRefresh  = 30 * time.Second
	stalePeerTimeout  = 2 * time.Second
	stalePeerParallel = 4
)

type staleWire struct {
	Key    string      `json:"key"`
	Env    string      `json:"env"`
	Status int         `json:"status"`
	Header http.Header `json:"header"`
	Stored time.Time   `json:"stored"`
}

type stalePeers struct {
	cache  *staleCache
	logger *slog.Logger
	client *http.Client
	port   string
	// list returns the host (IP) of every activator replica, self included.
	list func(ctx context.Context) ([]string, error)
	// self reports whether an IP belongs to this replica.
	self func(ip string) bool
	sem  chan struct{}

	mu  sync.Mutex
	ips []string
	at  time.Time
}

func newStalePeers(cache *staleCache, port string, list func(context.Context) ([]string, error), self func(string) bool, logger *slog.Logger) *stalePeers {
	return &stalePeers{
		cache:  cache,
		logger: logger,
		client: &http.Client{Timeout: stalePeerTimeout},
		port:   port,
		list:   list,
		self:   self,
		sem:    make(chan struct{}, stalePeerParallel),
	}
}

func (p *stalePeers) peerIPs(ctx context.Context, force bool) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if force || p.ips == nil || time.Since(p.at) > stalePeerRefresh {
		lctx, cancel := context.WithTimeout(ctx, stalePeerTimeout)
		ips, err := p.list(lctx)
		cancel()
		if err == nil {
			p.ips, p.at = ips, time.Now()
		} else {
			p.logger.Warn("activator: list stale peers", "err", err)
		}
	}
	return p.ips
}

// push sends a locally captured entry to every other replica. It never
// blocks the proxied response: past stalePeerParallel pushes in flight the
// copy simply stays local.
func (p *stalePeers) push(e *staleEntry) {
	select {
	case p.sem <- struct{}{}:
	default:
		return
	}
	go func() {
		defer func() { <-p.sem }()
		meta, err := json.Marshal(staleWire{Key: e.key, Env: e.env, Status: e.status, Header: e.header, Stored: e.stored})
		if err != nil {
			return
		}
		for _, ip := range p.peerIPs(context.Background(), false) {
			if p.self(ip) {
				continue
			}
			req, err := http.NewRequest(http.MethodPost, "http://"+net.JoinHostPort(ip, p.port)+stalePeerPath, bytes.NewReader(e.body))
			if err != nil {
				continue
			}
			req.Header.Set(stalePeerMetaHdr, base64.StdEncoding.EncodeToString(meta))
			resp, err := p.client.Do(req)
			if err != nil {
				p.logger.Debug("activator: push stale copy", "peer", ip, "err", err)
				continue
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()
}

func (p *stalePeers) known(ip string) bool {
	for _, force := range []bool{false, true} {
		for _, k := range p.peerIPs(context.Background(), force) {
			if k == ip {
				return true
			}
		}
	}
	return false
}

// ServeHTTP stores a copy pushed by a peer. Only activator replicas may
// write; a received copy is never pushed on.
func (p *stalePeers) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != stalePeerPath {
		http.NotFound(w, r)
		return
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !p.known(ip) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	raw, err := base64.StdEncoding.DecodeString(r.Header.Get(stalePeerMetaHdr))
	var m staleWire
	if err != nil || json.Unmarshal(raw, &m) != nil || m.Status != http.StatusOK || m.Env == "" || !strings.HasPrefix(m.Key, m.Env+"\x00") {
		http.Error(w, "bad copy", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, staleMaxEntry))
	if err != nil {
		http.Error(w, "bad body", http.StatusRequestEntityTooLarge)
		return
	}
	if now := time.Now(); m.Stored.IsZero() || m.Stored.After(now) {
		m.Stored = now
	}
	p.cache.put(&staleEntry{key: m.Key, env: m.Env, status: m.Status, header: m.Header, body: body, stored: m.Stored})
	w.WriteHeader(http.StatusNoContent)
}

// activatorPeerIPs lists the addresses behind the kuso-activator Service.
func (a *Activator) activatorPeerIPs(ns string) func(context.Context) ([]string, error) {
	return func(ctx context.Context) ([]string, error) {
		slices, err := a.kc.Clientset.DiscoveryV1().EndpointSlices(ns).List(ctx, metav1.ListOptions{
			LabelSelector: discoveryv1.LabelServiceName + "=kuso-activator",
		})
		if err != nil {
			return nil, err
		}
		var ips []string
		for _, s := range slices.Items {
			for _, ep := range s.Endpoints {
				ips = append(ips, ep.Addresses...)
			}
		}
		return ips, nil
	}
}

func localIPs() func(string) bool {
	set := map[string]bool{}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				set[n.IP.String()] = true
			}
		}
	}
	return func(ip string) bool { return set[ip] }
}

// ServeStalePeers runs the peer listener on addr (":3001") until ctx ends
// and turns on pushing captured copies to the other replicas in ns.
func (a *Activator) ServeStalePeers(ctx context.Context, addr, ns string) error {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("activator: peer addr %q: %w", addr, err)
	}
	p := newStalePeers(a.stale, port, a.activatorPeerIPs(ns), localIPs(), a.logger)
	push := p.push
	a.stale.onStore.Store(&push)
	srv := &http.Server{Addr: addr, Handler: p, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
