package projects

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	healthProbeTimeout    = 3 * time.Second
	healthProbeSnippetMax = 200
	healthProbePathMax    = 512
	// defaultServicePort mirrors the kusoenvironment chart's `port: 8080`
	// default for envs whose spec.port is unset.
	defaultServicePort = 8080
)

// HealthProbeRequest is the body of the health-check test endpoint. Path
// and Port are the (possibly unsaved) values from the settings form.
type HealthProbeRequest struct {
	Path string `json:"path"`
	Port int32  `json:"port,omitempty"`
	Env  string `json:"env,omitempty"`
}

// HealthProbeResult reports one GET against a pod. Error is set (and
// Status is 0) when the request never got a response.
type HealthProbeResult struct {
	Status      int    `json:"status,omitempty"`
	StatusText  string `json:"statusText,omitempty"`
	LatencyMs   int64  `json:"latencyMs"`
	BodySnippet string `json:"bodySnippet,omitempty"`
	// Location is the redirect target when the pod answered 3xx. The
	// redirect is reported, never followed.
	Location string `json:"location,omitempty"`
	Pod      string `json:"pod"`
	URL      string `json:"url"`
	Error    string `json:"error,omitempty"`
}

// ValidateProbePath accepts only an origin-relative path ("/healthz",
// "/ready?deep=1"). Anything that could change the host the request goes
// to — scheme-relative "//host", absolute URLs, userinfo — is rejected,
// as are control characters (header/request-line injection).
func ValidateProbePath(p string) error {
	switch {
	case p == "":
		return fmt.Errorf("%w: path is required", ErrInvalid)
	case len(p) > healthProbePathMax:
		return fmt.Errorf("%w: path is longer than %d characters", ErrInvalid, healthProbePathMax)
	case !strings.HasPrefix(p, "/"):
		return fmt.Errorf("%w: path must start with /", ErrInvalid)
	case strings.HasPrefix(p, "//"):
		return fmt.Errorf("%w: path must not start with //", ErrInvalid)
	case strings.ContainsAny(p, "@\\"):
		return fmt.Errorf("%w: path must not contain @ or \\", ErrInvalid)
	}
	for _, r := range p {
		if r < 0x21 || r == 0x7f || !utf8.ValidRune(r) || unicode.IsSpace(r) {
			return fmt.Errorf("%w: path must not contain spaces or control characters", ErrInvalid)
		}
	}
	ref, err := url.ParseRequestURI(p)
	if err != nil {
		return fmt.Errorf("%w: path is not a valid URL path", ErrInvalid)
	}
	if ref.Scheme != "" || ref.Host != "" || ref.User != nil {
		return fmt.Errorf("%w: path must be relative to the service", ErrInvalid)
	}
	return nil
}

// probeURL builds http://<ip>:<port><path> from parts. The host comes
// only from the pod IP; the user-supplied path contributes path + query.
func probeURL(podIP string, port int32, p string) (*url.URL, error) {
	ref, err := url.ParseRequestURI(p)
	if err != nil {
		return nil, fmt.Errorf("%w: path is not a valid URL path", ErrInvalid)
	}
	return &url.URL{
		Scheme:   "http",
		Host:     net.JoinHostPort(podIP, strconv.Itoa(int(port))),
		Path:     ref.Path,
		RawPath:  ref.RawPath,
		RawQuery: ref.RawQuery,
	}, nil
}

// allowedProbePorts is the set of ports a probe may target: the service
// port and the configured health-check port, from both the running env
// CR and the saved service spec (the form may be ahead of the rollout).
func allowedProbePorts(envPort, envHCPort, svcPort, svcHCPort int32) map[int32]bool {
	out := map[int32]bool{}
	for _, p := range []int32{envPort, envHCPort, svcPort, svcHCPort} {
		if p > 0 {
			out[p] = true
		}
	}
	if envPort == 0 {
		out[defaultServicePort] = true
	}
	return out
}

// pickProbePod returns a running, ready, not-terminating pod with an IP.
func pickProbePod(pods []corev1.Pod) *corev1.Pod {
	for i := range pods {
		p := &pods[i]
		if p.DeletionTimestamp != nil || p.Status.Phase != corev1.PodRunning || p.Status.PodIP == "" {
			continue
		}
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
				return p
			}
		}
	}
	return nil
}

// TestHealthcheck GETs req.Path on one of the service's own ready pods in
// the requested env, from the server, without following redirects.
// Errors: ErrInvalid (bad path/port), ErrNotFound (env not this
// service's), ErrConflict (no ready pod to probe).
func (s *Service) TestHealthcheck(ctx context.Context, project, service string, req HealthProbeRequest) (*HealthProbeResult, error) {
	if err := ValidateProbePath(req.Path); err != nil {
		return nil, err
	}
	ns, envCR, err := s.resolveOwnedEnv(ctx, project, service, req.Env)
	if err != nil {
		return nil, err
	}
	var envHC, svcPort, svcHC int32
	if envCR.Spec.Healthcheck != nil {
		envHC = envCR.Spec.Healthcheck.Port
	}
	if svc, err := s.Kube.GetKusoService(ctx, ns, envCR.Spec.Service); err == nil {
		svcPort = svc.Spec.Port
		if svc.Spec.Healthcheck != nil {
			svcHC = svc.Spec.Healthcheck.Port
		}
	}
	allowed := allowedProbePorts(envCR.Spec.Port, envHC, svcPort, svcHC)
	port := req.Port
	if port == 0 {
		switch {
		case envHC > 0:
			port = envHC
		case envCR.Spec.Port > 0:
			port = envCR.Spec.Port
		default:
			port = defaultServicePort
		}
	}
	if !allowed[port] {
		return nil, fmt.Errorf("%w: port %d is neither the service port nor the health-check port", ErrInvalid, port)
	}

	pods, err := s.Kube.Clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/instance=" + envCR.Name,
	})
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	pod := pickProbePod(pods.Items)
	if pod == nil {
		return nil, fmt.Errorf("%w: no running, ready pod to test — start or deploy the service first", ErrConflict)
	}
	u, err := probeURL(pod.Status.PodIP, port, req.Path)
	if err != nil {
		return nil, err
	}
	res := probeHTTP(ctx, u)
	res.Pod = pod.Name
	return res, nil
}

// probeHTTP performs the GET. Transport failures land in Error, not in
// the returned error: the probe ran, the target didn't answer.
func probeHTTP(ctx context.Context, u *url.URL) *HealthProbeResult {
	res := &HealthProbeResult{URL: u.String()}
	ctx, cancel := context.WithTimeout(ctx, healthProbeTimeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		res.Error = "could not build the request"
		return res
	}
	httpReq.Header.Set("User-Agent", "kuso-healthcheck-test")
	client := &http.Client{
		Timeout: healthProbeTimeout,
		// Proxy nil: a pod IP must be dialled directly, never through an
		// HTTP(S)_PROXY from the server's environment.
		Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	start := time.Now()
	resp, err := client.Do(httpReq)
	res.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		res.Error = describeProbeError(err)
		return res
	}
	defer resp.Body.Close()
	res.Status = resp.StatusCode
	res.StatusText = http.StatusText(resp.StatusCode)
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		res.Location = resp.Header.Get("Location")
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, healthProbeSnippetMax))
	res.BodySnippet = sanitizeSnippet(b)
	return res
}

// sanitizeSnippet turns raw body bytes into display-safe text: invalid
// UTF-8 (including a rune cut by the byte limit) becomes U+FFFD and
// control characters other than newline/tab become spaces.
func sanitizeSnippet(b []byte) string {
	s := strings.ToValidUTF8(string(b), "�")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

func describeProbeError(err error) string {
	var nerr net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &nerr) && nerr.Timeout()):
		return fmt.Sprintf("no response within %s", healthProbeTimeout)
	case strings.Contains(err.Error(), "connection refused"):
		return "connection refused — nothing is listening on that port"
	default:
		return "request failed: " + err.Error()
	}
}
