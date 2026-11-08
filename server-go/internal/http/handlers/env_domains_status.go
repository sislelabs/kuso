package handlers

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"kuso/server/internal/db"
	"kuso/server/internal/hostcheck"
	"kuso/server/internal/kube"
)

const (
	domainStatusTTL        = 30 * time.Second
	domainStatusMaxEntries = 512
	domainStatusDNSTimeout = 3 * time.Second
	domainStatusBudget     = 10 * time.Second
	// domainStatusFreshFloor: a ?fresh=1 request ("Check again") skips a
	// cached result older than this, so repeated clicks still can't
	// fan out DNS lookups faster than once per floor.
	domainStatusFreshFloor = 5 * time.Second
)

type domainKind string

const (
	domainKindAuto     domainKind = "auto"
	domainKindCustom   domainKind = "custom"
	domainKindWildcard domainKind = "wildcard"
)

type tlsStatus string

const (
	tlsReady    tlsStatus = "ready"
	tlsIssuing  tlsStatus = "issuing"
	tlsFailed   tlsStatus = "failed"
	tlsMissing  tlsStatus = "missing"
	tlsExternal tlsStatus = "external"
	// tlsUnknown: the Certificate list itself failed, so nothing can be
	// said about the host's cert.
	tlsUnknown tlsStatus = "unknown"
)

const dnsSkipped hostcheck.DNSStatus = "skipped"

type domainDNS struct {
	Status   hostcheck.DNSStatus `json:"status"`
	Resolved []string            `json:"resolved"`
	Message  string              `json:"message,omitempty"`
}

// tlsCause is the likely reason a cert isn't serving yet, so the UI can
// say it in plain words instead of echoing cert-manager conditions.
type tlsCause string

const (
	causeCertManagerMissing tlsCause = "cert_manager_missing"
	causeRateLimited        tlsCause = "rate_limited"
	causeCAA                tlsCause = "caa"
	causeDNSNotPointed      tlsCause = "dns_not_pointed"
	causeCloudflareProxy    tlsCause = "cloudflare_proxy"
	causeExpired            tlsCause = "expired"
	causeValidationFailed   tlsCause = "validation_failed"
	causeIssuing            tlsCause = "issuing"
	causeNotRequested       tlsCause = "not_requested"
)

type domainTLS struct {
	Status   tlsStatus `json:"status"`
	NotAfter string    `json:"notAfter,omitempty"`
	Reason   string    `json:"reason,omitempty"`
	Message  string    `json:"message,omitempty"`
	Cause    tlsCause  `json:"cause,omitempty"`
	// Since is when the cert last stopped being Ready (or was created):
	// how long issuance has been going on.
	Since       string `json:"since,omitempty"`
	LastFailure string `json:"lastFailure,omitempty"`
	Attempts    int    `json:"attempts,omitempty"`
	// failure is the raw cert-manager text the cause is matched against.
	failure string
}

type domainStatus struct {
	Host string     `json:"host"`
	Kind domainKind `json:"kind"`
	DNS  domainDNS  `json:"dns"`
	TLS  domainTLS  `json:"tls"`
}

type envDomainsStatusResponse struct {
	CheckedAt   string         `json:"checkedAt"`
	ExpectedIPs []string       `json:"expectedIPs"`
	Domains     []domainStatus `json:"domains"`
}

type envDomain struct {
	host string
	kind domainKind
}

// envDomainsOf lists every host the env serves, deduped, in the order the
// UI shows them: the auto host, then custom domains, then wildcards.
func envDomainsOf(env *kube.KusoEnvironment) []envDomain {
	seen := map[string]bool{}
	var out []envDomain
	add := func(h string, k domainKind) {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" || seen[h] {
			return
		}
		seen[h] = true
		out = append(out, envDomain{host: h, kind: k})
	}
	add(env.Spec.Host, domainKindAuto)
	for _, h := range env.Spec.AdditionalHosts {
		add(h, domainKindCustom)
	}
	for _, w := range env.Spec.WildcardDomains {
		add(w.Host, domainKindWildcard)
	}
	return out
}

// tlsFor classifies host's cert from the env namespace's Certificates.
// When several cover the host, a Ready one wins.
func tlsFor(d envDomain, certs []hostcheck.Cert, now time.Time) domainTLS {
	if d.kind == domainKindWildcard {
		return domainTLS{Status: tlsExternal}
	}
	var match *hostcheck.Cert
	for i := range certs {
		if !certCovers(certs[i], d.host) {
			continue
		}
		if match == nil || (!match.Ready && certs[i].Ready) {
			match = &certs[i]
		}
	}
	if match == nil {
		return domainTLS{Status: tlsMissing}
	}
	out := domainTLS{Attempts: match.FailedAttempts}
	if match.NotAfter != nil {
		out.NotAfter = match.NotAfter.UTC().Format(time.RFC3339)
	}
	if match.LastFailure != nil {
		out.LastFailure = match.LastFailure.UTC().Format(time.RFC3339)
	}
	if !match.Ready {
		since := match.ReadyTransition
		if since.IsZero() {
			since = match.Created
		}
		if !since.IsZero() {
			out.Since = since.UTC().Format(time.RFC3339)
		}
	}
	out.failure = strings.Join([]string{match.IssuingFailure, match.Reason, match.Message}, " ")
	if match.NotAfter != nil && !match.NotAfter.After(now) {
		out.Status = tlsFailed
		out.Reason = "Expired"
		out.Message = "certificate expired " + match.NotAfter.UTC().Format("2006-01-02")
		return out
	}
	if match.Ready {
		out.Status = tlsReady
		return out
	}
	// days=0: only "not Ready past the issuance grace" counts here; expiry
	// was handled above.
	if problem, bad := hostcheck.CertProblem(*match, now, 0); bad {
		out.Status = tlsFailed
		out.Reason = match.Reason
		out.Message = match.Message
		if out.Message == "" {
			out.Message = problem
		}
		return out
	}
	out.Status = tlsIssuing
	out.Reason = match.Reason
	out.Message = match.Message
	return out
}

// diagnoseTLS picks the most useful single explanation for a host whose
// cert isn't serving. Order matters: a rate limit or CAA block outlives a
// DNS fix, so it's reported first; DNS beats the generic failure because
// it's the usual root cause and the one the user can act on.
func diagnoseTLS(tls domainTLS, dns hostcheck.DNSStatus, certManagerMissing bool) tlsCause {
	if tls.Status == tlsReady || tls.Status == tlsExternal || tls.Status == tlsUnknown {
		return ""
	}
	if certManagerMissing {
		return causeCertManagerMissing
	}
	text := strings.ToLower(tls.failure)
	switch {
	case containsAny(text, "ratelimited", "rate limit", "too many certificates", "too many failed authorizations", "too many new orders"):
		return causeRateLimited
	case containsAny(text, "error:caa", "caa record"):
		return causeCAA
	case dns == hostcheck.DNSMismatch || dns == hostcheck.DNSUnresolved:
		return causeDNSNotPointed
	}
	switch tls.Status {
	case tlsFailed:
		if tls.Reason == "Expired" {
			return causeExpired
		}
		if dns == hostcheck.DNSProxied {
			return causeCloudflareProxy
		}
		return causeValidationFailed
	case tlsIssuing:
		return causeIssuing
	case tlsMissing:
		return causeNotRequested
	}
	return ""
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func certCovers(c hostcheck.Cert, host string) bool {
	for _, n := range c.DNSNames {
		if strings.EqualFold(strings.TrimSpace(n), host) {
			return true
		}
	}
	return false
}

type domainStatusCacheEntry struct {
	resp    envDomainsStatusResponse
	stored  time.Time
	expires time.Time
}

// domainStatusCache keeps DNS lookups + cert lists off the hot path when
// the UI polls. Zero value is ready to use.
type domainStatusCache struct {
	mu      sync.Mutex
	entries map[string]domainStatusCacheEntry
}

func (c *domainStatusCache) get(key string, now time.Time, fresh bool) (envDomainsStatusResponse, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || !now.Before(e.expires) || (fresh && now.Sub(e.stored) >= domainStatusFreshFloor) {
		return envDomainsStatusResponse{}, false
	}
	return e.resp, true
}

func (c *domainStatusCache) put(key string, resp envDomainsStatusResponse, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]domainStatusCacheEntry{}
	}
	if len(c.entries) >= domainStatusMaxEntries {
		for k, e := range c.entries {
			if !now.Before(e.expires) {
				delete(c.entries, k)
			}
		}
		// Still full of live entries: evict arbitrarily rather than grow.
		for k := range c.entries {
			if len(c.entries) < domainStatusMaxEntries {
				break
			}
			delete(c.entries, k)
		}
	}
	c.entries[key] = domainStatusCacheEntry{resp: resp, stored: now, expires: now.Add(domainStatusTTL)}
}

// GET /api/projects/{project}/services/{service}/envs/{env}/domains/status
// Per-host DNS + TLS state for one env. ?fresh=1 bypasses the cache
// (down to domainStatusFreshFloor) for an explicit re-check. {env} is the short env name
// ("production", "staging", "pr-12"), resolved to the CR the same way the
// PUT/POST/DELETE .../envs/{env}/domains handlers do.
func (h *ProjectsHandler) GetEnvDomainsStatus(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	service := chi.URLParam(r, "service")
	envName := chi.URLParam(r, "env")

	ctx, cancel := projectCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleViewer) {
		return
	}
	env, err := h.Svc.GetEnvironment(ctx, project, envStatusCRName(project, service, envName))
	if err != nil {
		h.fail(w, "get env domains status", err)
		return
	}
	// The host list is part of the key so a just-added domain gets checked
	// straight away instead of waiting out the TTL.
	key := domainStatusKey(project, service, envName, env)
	fresh := r.URL.Query().Get("fresh") == "1"
	if resp, ok := h.domainStatus.get(key, time.Now(), fresh); ok {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// DNS lookups can outlast projectCtx's 5s across many hosts; give the
	// checks their own budget off the request context.
	cctx, ccancel := context.WithTimeout(r.Context(), domainStatusBudget)
	defer ccancel()
	resp := h.buildDomainsStatus(cctx, env, time.Now(), net.DefaultResolver)
	h.domainStatus.put(key, resp, time.Now())
	writeJSON(w, http.StatusOK, resp)
}

func domainStatusKey(project, service, envName string, env *kube.KusoEnvironment) string {
	parts := []string{project, service, envName}
	for _, d := range envDomainsOf(env) {
		parts = append(parts, string(d.kind)+":"+d.host)
	}
	return strings.Join(parts, "\x00")
}

// envStatusCRName mirrors projects.envCRNameFor: the service may arrive
// short ("web") or already prefixed ("shop-web").
func envStatusCRName(project, service, envName string) string {
	return fmt.Sprintf("%s-%s-%s", project, strings.TrimPrefix(service, project+"-"), envName)
}

func (h *ProjectsHandler) buildDomainsStatus(ctx context.Context, env *kube.KusoEnvironment, now time.Time, resolver hostcheck.Resolver) (resp envDomainsStatusResponse) {
	domains := envDomainsOf(env)
	certManagerMissing := false
	// Runs on every return path below, after DNS (if any) is known.
	defer func() {
		for i := range resp.Domains {
			d := &resp.Domains[i]
			d.TLS.Cause = diagnoseTLS(d.TLS, d.DNS.Status, certManagerMissing)
		}
	}()
	resp = envDomainsStatusResponse{
		CheckedAt:   now.UTC().Format(time.RFC3339),
		ExpectedIPs: []string{},
		Domains:     make([]domainStatus, len(domains)),
	}
	for i, d := range domains {
		resp.Domains[i] = domainStatus{Host: d.host, Kind: d.kind, DNS: domainDNS{Status: dnsSkipped, Resolved: []string{}}}
	}

	var certs []hostcheck.Cert
	certErr := error(nil)
	if h.Kube == nil || h.Kube.Dynamic == nil {
		certErr = fmt.Errorf("no kube client")
	} else if certs, certErr = hostcheck.ListCertificatesIn(ctx, h.Kube.Dynamic, env.Namespace); certErr != nil && apierrors.IsNotFound(certErr) {
		// cert-manager not installed: no Certificates exist, so every
		// non-wildcard host is genuinely "missing".
		certErr = nil
		certManagerMissing = true
	}
	if certErr != nil {
		h.Logger.Warn("env domains status: list certificates", "namespace", env.Namespace, "err", certErr)
	}
	for i, d := range domains {
		if certErr != nil && d.kind != domainKindWildcard {
			resp.Domains[i].TLS = domainTLS{Status: tlsUnknown, Message: "could not list certificates"}
			continue
		}
		resp.Domains[i].TLS = tlsFor(d, certs, now)
	}

	if env.Spec.Internal {
		return resp
	}
	var idx []int
	var hosts []string
	for i, d := range domains {
		if hostcheck.Checkable(d.host) {
			idx = append(idx, i)
			hosts = append(hosts, d.host)
		}
	}
	if len(hosts) == 0 {
		return resp
	}
	if h.Kube == nil || h.Kube.Clientset == nil {
		return resp
	}
	expected, err := hostcheck.ExpectedIngressIPs(ctx, h.Kube.Clientset, os.Getenv("KUSO_INGRESS_IPS"))
	if err != nil || len(expected) == 0 {
		if err != nil {
			h.Logger.Warn("env domains status: expected ingress IPs", "err", err)
		}
		for _, i := range idx {
			resp.Domains[i].DNS.Message = "cluster ingress IPs unknown; set KUSO_INGRESS_IPS"
		}
		return resp
	}
	resp.ExpectedIPs = expected
	results := hostcheck.CheckHosts(ctx, resolver, hosts, expected, domainStatusDNSTimeout)
	for j, res := range results {
		d := domainDNS{Status: res.Status, Resolved: res.Resolved}
		if d.Resolved == nil {
			d.Resolved = []string{}
		}
		if res.Err != nil {
			d.Message = res.Err.Error()
		}
		resp.Domains[idx[j]].DNS = d
	}
	return resp
}
