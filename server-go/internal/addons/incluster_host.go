package addons

import (
	"net"
	"net/url"
	"strings"
)

// QualifyInClusterHost rewrites a bare in-cluster Service name so it
// resolves from kuso-server's own namespace. It accepts a host, a
// host:port, or a URL/DSN and returns the input with only the host part
// changed ("e2e-db" → "e2e-db.<ns>.svc").
//
// Addon conn Secrets carry SHORT hosts on purpose: the project's pods
// share the addon's namespace and must keep resolving them unqualified.
// kuso-server runs in the home namespace, so for a project with its own
// execution namespace the short name misses DNS entirely. Every
// server-side dial of an addon (SQL browser, backup download, ClickHouse
// runner, addon S3) goes through here. The Secrets are never rewritten.
//
// Anything already dotted (FQDN, "<svc>.<ns>"), an IP, localhost, or an
// unparseable value passes through untouched, as does everything when
// ns is empty. `.svc` rather than `.svc.cluster.local` so a non-default
// cluster domain still resolves through the pod's search path.
func QualifyInClusterHost(addr, ns string) string {
	if addr == "" || ns == "" {
		return addr
	}
	if strings.Contains(addr, "://") {
		u, err := url.Parse(addr)
		if err != nil || u.Host == "" {
			return addr
		}
		host := u.Hostname()
		q := qualifyBareHost(host, ns)
		if q == host {
			return addr
		}
		if port := u.Port(); port != "" {
			u.Host = net.JoinHostPort(q, port)
		} else {
			u.Host = q
		}
		return u.String()
	}
	if host, port, err := net.SplitHostPort(addr); err == nil {
		q := qualifyBareHost(host, ns)
		if q == host {
			return addr
		}
		return net.JoinHostPort(q, port)
	}
	return qualifyBareHost(addr, ns)
}

func qualifyBareHost(host, ns string) string {
	if host == "" || host == "localhost" || strings.ContainsAny(host, ".:") || net.ParseIP(host) != nil {
		return host
	}
	return host + "." + ns + ".svc"
}
