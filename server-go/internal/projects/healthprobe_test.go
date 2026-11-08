package projects

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

func TestValidateProbePath(t *testing.T) {
	t.Parallel()
	ok := []string{"/", "/healthz", "/api/health?deep=1", "/a%20b"}
	// Each bad input names the guard that must reject it, so removing a
	// guard fails here even when a later check would also catch it.
	bad := map[string]string{
		"":                             "required",
		"healthz":                      "must start with /",
		"http://evil.example/":         "must start with /",
		"//evil.example/x":             "must not start with //",
		"/x@evil":                      "must not contain @",
		"/x\\y":                        "must not contain @",
		"/x\r\nHost: evil":             "control characters",
		"/a b":                         "control characters",
		"/\t":                          "control characters",
		"/" + strings.Repeat("a", 600): "longer than",
	}
	for _, p := range ok {
		if err := ValidateProbePath(p); err != nil {
			t.Errorf("ValidateProbePath(%q) = %v, want ok", p, err)
		}
	}
	for p, want := range bad {
		err := ValidateProbePath(p)
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), want) {
			t.Errorf("ValidateProbePath(%q) = %v, want ErrInvalid mentioning %q", p, err, want)
		}
	}
}

func TestProbeURL_HostComesFromPodIPOnly(t *testing.T) {
	t.Parallel()
	u, err := probeURL("10.42.0.7", 8080, "/ready?x=1")
	if err != nil {
		t.Fatal(err)
	}
	if got := u.String(); got != "http://10.42.0.7:8080/ready?x=1" {
		t.Fatalf("url = %q", got)
	}
}

// probeFixture seeds project alpha/web (production) whose env + pod point
// at a local httptest server, plus a beta/web env with its own ready pod.
func probeFixture(t *testing.T, handler http.Handler) (*Service, int32) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	_, portStr, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	p, _ := strconv.Atoi(portStr)
	port := int32(p)

	env := func(project string) seed {
		name := project + "-web-production"
		return typedSeed(kube.GVREnvironments, "KusoEnvironment", name, &kube.KusoEnvironment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso"},
			Spec:       kube.KusoEnvironmentSpec{Project: project, Service: project + "-web", Port: port},
		})
	}
	pod := func(name, instance string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso",
				Labels: map[string]string{"app.kubernetes.io/instance": instance}},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning, PodIP: "127.0.0.1",
				Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
			},
		}
	}
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}), seedProject("beta", kube.KusoProjectSpec{}),
		env("alpha"), env("beta"),
	)
	s.Kube.Clientset = kubefake.NewSimpleClientset(
		pod("alpha-web-production-a", "alpha-web-production"),
		pod("beta-web-production-b", "beta-web-production"),
	)
	return s, port
}

func TestTestHealthcheck_ProbesOwnPod(t *testing.T) {
	t.Parallel()
	s, port := probeFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("ok\x00\x1b[31m" + strings.Repeat("x", 400)))
	}))
	res, err := s.TestHealthcheck(context.Background(), "alpha", "web", HealthProbeRequest{Path: "/healthz"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != 200 || res.Pod != "alpha-web-production-a" || res.Error != "" {
		t.Fatalf("result = %+v", res)
	}
	if !strings.HasPrefix(res.BodySnippet, "ok  [31m") || len(res.BodySnippet) != healthProbeSnippetMax {
		t.Fatalf("snippet = %q (len %d), want sanitized 200-byte prefix", res.BodySnippet, len(res.BodySnippet))
	}
	if res.URL != "http://127.0.0.1:"+strconv.Itoa(int(port))+"/healthz" {
		t.Fatalf("url = %q", res.URL)
	}
}

func TestTestHealthcheck_RejectsForeignPort(t *testing.T) {
	t.Parallel()
	s, port := probeFixture(t, http.NotFoundHandler())
	_, err := s.TestHealthcheck(context.Background(), "alpha", "web", HealthProbeRequest{Path: "/", Port: port + 1})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid for a port that is neither service nor health-check port", err)
	}
}

func TestTestHealthcheck_RejectsInvalidPathBeforeAnyKubeRead(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	s, _ := probeFixture(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	_, err := s.TestHealthcheck(context.Background(), "alpha", "web", HealthProbeRequest{Path: "//evil.example/"})
	if !errors.Is(err, ErrInvalid) || hits.Load() != 0 {
		t.Fatalf("err = %v hits = %d, want ErrInvalid and no request", err, hits.Load())
	}
}

// An env belonging to another project must not be probeable through
// ?env=<fqn> — the route only checked access to alpha.
func TestTestHealthcheck_RejectsOtherServicesEnv(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	s, _ := probeFixture(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	_, err := s.TestHealthcheck(context.Background(), "alpha", "web",
		HealthProbeRequest{Path: "/", Env: "beta-web-production"})
	if !errors.Is(err, ErrNotFound) || hits.Load() != 0 {
		t.Fatalf("err = %v hits = %d, want ErrNotFound and no request", err, hits.Load())
	}
}

func TestTestHealthcheck_DoesNotFollowRedirects(t *testing.T) {
	t.Parallel()
	var followed atomic.Int32
	s, _ := probeFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/target" {
			followed.Add(1)
			return
		}
		http.Redirect(w, r, "/target", http.StatusFound)
	}))
	res, err := s.TestHealthcheck(context.Background(), "alpha", "web", HealthProbeRequest{Path: "/start"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != http.StatusFound || res.Location != "/target" || followed.Load() != 0 {
		t.Fatalf("result = %+v followed = %d, want 302 reported, not followed", res, followed.Load())
	}
}

func TestTestHealthcheck_NoReadyPod(t *testing.T) {
	t.Parallel()
	s, _ := probeFixture(t, http.NotFoundHandler())
	s.Kube.Clientset = kubefake.NewSimpleClientset()
	_, err := s.TestHealthcheck(context.Background(), "alpha", "web", HealthProbeRequest{Path: "/"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

func TestAllowedProbePorts(t *testing.T) {
	t.Parallel()
	got := allowedProbePorts(0, 9090, 3000, 0)
	for _, p := range []int32{8080, 9090, 3000} {
		if !got[p] {
			t.Errorf("port %d not allowed, want allowed", p)
		}
	}
	if got[22] || got[0] {
		t.Errorf("unexpected ports allowed: %v", got)
	}
}
