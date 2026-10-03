package uptime

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestTargetURLKeepsHost(t *testing.T) {
	const host = "shop-web-production.kuso.svc.cluster.local"
	cases := map[string]string{
		"":                       "http://" + host + "/",
		"/":                      "http://" + host + "/",
		"/health":                "http://" + host + "/health",
		"health":                 "http://" + host + "/health",
		"/health?deep=1":         "http://" + host + "/health?deep=1",
		"//evil.com/x":           "", // host checked below
		"http://evil.com/x":      "",
		"https://evil.com:8443/": "",
		"/\\evil.com":            "",
		"@evil.com/x":            "",
		"/a/../../b":             "",
	}
	for in, want := range cases {
		got := TargetURL("kuso", "shop-web-production", in)
		u, err := url.Parse(got)
		if err != nil {
			t.Fatalf("%q → unparseable %q: %v", in, got, err)
		}
		if u.Host != host || u.Scheme != "http" || u.User != nil {
			t.Errorf("%q → %q: host %q scheme %q user %v", in, got, u.Host, u.Scheme, u.User)
		}
		if want != "" && got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}

func TestProbeStatuses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != userAgent {
			t.Errorf("user agent = %q", r.Header.Get("User-Agent"))
		}
		switch r.URL.Path {
		case "/ok":
			w.WriteHeader(200)
		case "/redirect":
			http.Redirect(w, r, "/boom", http.StatusFound)
		case "/missing":
			w.WriteHeader(404)
		case "/boom":
			w.WriteHeader(500)
		case "/bad-gateway":
			w.WriteHeader(502)
		}
	}))
	defer srv.Close()
	p := NewProber(2 * time.Second)
	for path, wantOK := range map[string]bool{"/ok": true, "/redirect": true, "/missing": true, "/boom": false, "/bad-gateway": false} {
		r := p.Probe(context.Background(), srv.URL+path)
		if r.OK != wantOK {
			t.Errorf("%s: OK = %v, want %v (%+v)", path, r.OK, wantOK, r)
		}
		if !wantOK && !strings.HasPrefix(r.Error, "HTTP 5") {
			t.Errorf("%s: error = %q", path, r.Error)
		}
	}
	// The redirect target is /boom (500): had we followed it, /redirect
	// would have failed above.
}

func TestProbeHangTimesOut(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	p := NewProber(300 * time.Millisecond)
	start := time.Now()
	r := p.Probe(context.Background(), srv.URL)
	if r.OK || !strings.HasPrefix(r.Error, "timed out") {
		t.Fatalf("hang: %+v", r)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("probe took %v", time.Since(start))
	}
}

func TestProbeClosedPort(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	p := NewProber(time.Second)
	r := p.Probe(context.Background(), "http://"+addr+"/")
	if r.OK || r.Error != "connection refused" {
		t.Fatalf("closed port: %+v", r)
	}
	if p.Reachable(context.Background(), "http://"+addr+"/") {
		t.Fatal("closed port reads as reachable")
	}
}

func TestReachableOn503(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer srv.Close()
	if !NewProber(time.Second).Reachable(context.Background(), srv.URL) {
		t.Fatal("a 503 answer must count as reachable")
	}
}
