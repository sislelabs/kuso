package uptime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	userAgent    = "kuso-uptime/1"
	maxBodyBytes = 64 << 10
)

// Result is one check's outcome. Error is a short class ("timed out
// after 10s", "connection refused", "HTTP 502"), never response content.
type Result struct {
	OK         bool
	StatusCode int
	Latency    time.Duration
	Error      string
}

// TargetURL builds the in-cluster URL for an env. The host always comes
// from the env's name and namespace; only the path and query of
// userPath are used, so a user-set path can't point the check elsewhere.
func TargetURL(namespace, env, userPath string) string {
	u := url.URL{
		Scheme: "http",
		Host:   env + "." + namespace + ".svc.cluster.local",
		Path:   "/",
	}
	if p := strings.TrimSpace(userPath); p != "" {
		if parsed, err := url.Parse(p); err == nil {
			// "//evil.com/x" and "http://evil/x" parse with a host; only
			// the path and query are taken.
			u.Path = "/" + strings.TrimLeft(parsed.Path, "/")
			u.RawQuery = parsed.RawQuery
		}
	}
	return u.String()
}

type Prober struct {
	client  *http.Client
	timeout time.Duration
}

func NewProber(timeout time.Duration) *Prober {
	if timeout <= 0 {
		timeout = ProbeTimeout
	}
	return &Prober{
		timeout: timeout,
		client: &http.Client{
			Timeout: timeout,
			// A redirect is an answer; following it could leave the cluster.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			Transport: &http.Transport{
				// A fresh connection per check, so kube-proxy picks a pod
				// each time and a dead keep-alive can't read as an outage.
				DisableKeepAlives:   true,
				Proxy:               nil,
				TLSHandshakeTimeout: timeout,
				DialContext:         (&net.Dialer{Timeout: timeout}).DialContext,
			},
		},
	}
}

// Probe GETs target. Any status below 500 is ok.
func (p *Prober) Probe(ctx context.Context, target string) Result {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return Result{Error: "bad url"}
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := p.client.Do(req)
	if err != nil {
		return Result{Latency: time.Since(start), Error: p.classify(err)}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBodyBytes))
	r := Result{StatusCode: resp.StatusCode, Latency: time.Since(start)}
	if resp.StatusCode >= 500 {
		r.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		return r
	}
	r.OK = true
	return r
}

// Reachable reports whether target answered at all, whatever the status.
func (p *Prober) Reachable(ctx context.Context, target string) bool {
	r := p.Probe(ctx, target)
	return r.StatusCode != 0
}

func (p *Prober) classify(err error) string {
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return "timed out after " + p.timeout.String()
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "connection refused"):
		return "connection refused"
	case strings.Contains(msg, "no such host"):
		return "service not found in DNS"
	case strings.Contains(msg, "connection reset"):
		return "connection reset"
	case strings.Contains(msg, "EOF"):
		return "connection closed without a response"
	}
	return "connection failed"
}
