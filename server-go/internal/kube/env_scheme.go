package kube

import "strings"

// reservedTLDs are the suffixes the kusoenvironment chart never requests
// a certificate for (its Ingress template carries the same list). A host
// under one of them is served over plain HTTP whatever tlsEnabled says.
var reservedTLDs = map[string]bool{
	"local": true, "internal": true, "localhost": true, "test": true,
	"example": true, "invalid": true, "arpa": true,
}

// SchemeFor returns the scheme host is actually served on: "https" when
// TLS is on and the chart will request a cert for it, else "http". Every
// public URL kuso prints goes through this, so a link never promises
// HTTPS on a host that has no certificate.
func (s KusoEnvironmentSpec) SchemeFor(host string) string {
	if !s.TLSEnabled {
		return "http"
	}
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if i := strings.LastIndexByte(host, '.'); i >= 0 && reservedTLDs[host[i+1:]] {
		return "http"
	}
	return "https"
}
