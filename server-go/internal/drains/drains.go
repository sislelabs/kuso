// Package drains forwards the platform's app logs to external sinks:
// a generic HTTP JSON batch endpoint, an OTLP/HTTP logs collector, or a
// Loki push API. Lines come from logship's Tap (exactly what LogLine
// stores, post rate cap), so a drain never sees more or less than the
// in-app log viewer.
//
// Delivery runs inside the cluster-singletons leader lease alongside
// logship, so each line is shipped once no matter how many kuso-server
// replicas run. The one duplicate window is leader failover: logship
// resumes with a 2s overlap, and whatever sat in a drain's buffer on
// the old leader is lost.
package drains

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"kuso/server/internal/httpx"
)

// Type is the wire protocol a drain speaks.
type Type string

const (
	TypeHTTP Type = "http"
	TypeOTLP Type = "otlp"
	TypeLoki Type = "loki"
)

// ErrInvalid marks a drain config that failed validation.
var ErrInvalid = errors.New("invalid drain")

// ErrNotFound is returned by stores for an unknown drain id.
var ErrNotFound = errors.New("drain not found")

// Drain is one configured sink. Project "" means instance-wide.
type Drain struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Type      Type              `json:"type"`
	URL       string            `json:"url"`
	Project   string            `json:"project,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Secret    string            `json:"secret,omitempty"`
	Enabled   bool              `json:"enabled"`
	CreatedAt time.Time         `json:"createdAt"`
	UpdatedAt time.Time         `json:"updatedAt"`
	CreatedBy string            `json:"createdBy,omitempty"`
}

// Matches reports whether a line from project should go to this drain.
func (d Drain) Matches(project string) bool {
	return d.Enabled && (d.Project == "" || d.Project == project)
}

// Line is one log line as shipped. Ts is the kubelet emit time,
// Observed the time kuso ingested it.
type Line struct {
	Ts       time.Time
	Observed time.Time
	Project  string
	Service  string
	Env      string
	EnvKind  string
	Pod      string
	Line     string
}

// reservedHeaders are set by the sender and must not be overridden.
var reservedHeaders = map[string]bool{
	"host": true, "content-type": true, "content-length": true,
	"transfer-encoding": true, "connection": true,
	"x-kuso-signature": true, "x-hub-signature-256": true, "x-kuso-timestamp": true,
}

// Normalize validates d and returns the canonical form: credentials in
// the URL's userinfo move to an Authorization: Basic header (so the URL
// is never a secret), and an empty name defaults to the host.
func Normalize(d Drain) (Drain, error) {
	switch d.Type {
	case TypeHTTP, TypeOTLP, TypeLoki:
	default:
		return d, fmt.Errorf("%w: type must be http, otlp or loki (got %q)", ErrInvalid, d.Type)
	}
	if strings.TrimSpace(d.URL) == "" {
		return d, fmt.Errorf("%w: url is required", ErrInvalid)
	}
	u, err := url.Parse(strings.TrimSpace(d.URL))
	if err != nil {
		return d, fmt.Errorf("%w: url: %v", ErrInvalid, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return d, fmt.Errorf("%w: url scheme must be http or https", ErrInvalid)
	}
	// Same pre-dial check redirect hops get: literal localhost / reserved
	// IPs fail here with a clear message; hostnames are re-checked at
	// dial time by the SSRF-safe transport.
	if err := httpx.ValidateRedirectTarget(u); err != nil {
		return d, fmt.Errorf("%w: url: %v", ErrInvalid, strings.TrimPrefix(err.Error(), "httpx: refusing redirect to "))
	}
	headers := make(map[string]string, len(d.Headers)+1)
	for k, v := range d.Headers {
		k = strings.TrimSpace(k)
		if !validHeaderName(k) {
			return d, fmt.Errorf("%w: header name %q is not a valid HTTP token", ErrInvalid, k)
		}
		if reservedHeaders[strings.ToLower(k)] {
			return d, fmt.Errorf("%w: header %q is set by kuso and cannot be overridden", ErrInvalid, k)
		}
		if strings.ContainsAny(v, "\r\n") {
			return d, fmt.Errorf("%w: header %q value contains a newline", ErrInvalid, k)
		}
		headers[k] = v
	}
	if u.User != nil {
		pass, _ := u.User.Password()
		cred := u.User.Username() + ":" + pass
		headers["Authorization"] = "Basic " + base64.StdEncoding.EncodeToString([]byte(cred))
		u.User = nil
	}
	if len(headers) == 0 {
		headers = nil
	}
	d.Headers = headers
	d.URL = u.String()
	d.Name = strings.TrimSpace(d.Name)
	if d.Name == "" {
		d.Name = string(d.Type) + " → " + u.Host
	}
	return d, nil
}

func validHeaderName(k string) bool {
	if k == "" {
		return false
	}
	for _, c := range k {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.ContainsRune("!#$%&'*+-.^_`|~", c):
		default:
			return false
		}
	}
	return true
}

// Endpoint returns the URL a batch is POSTed to. OTLP and Loki accept a
// base URL (what collectors and Grafana Cloud hand out) and get their
// protocol path appended; a URL already ending in it is left alone.
func Endpoint(t Type, raw string) string {
	var suffix string
	switch t {
	case TypeOTLP:
		suffix = "/v1/logs"
	case TypeLoki:
		suffix = "/loki/api/v1/push"
	default:
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if strings.HasSuffix(u.Path, suffix) {
		return raw
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + suffix
	return u.String()
}
