package alerts

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuso/server/internal/db"
	"kuso/server/internal/hostcheck"
	"kuso/server/internal/kube"
	"kuso/server/internal/notify"
)

// ---------------------------------------------------------------------------
// decideEpisode: the pure fire-once / resolve state machine.
// ---------------------------------------------------------------------------

func TestDecideEpisode(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { v := now.Add(-d); return &v }
	breach := func(targets ...string) finding {
		f := finding{details: map[string]string{}}
		for _, t := range targets {
			f.targets = append(f.targets, t)
			f.details[t] = t + " is bad"
		}
		return f
	}
	cases := []struct {
		name       string
		rule       db.AlertRule
		f          finding
		wantAction episodeAction
		wantNew    []string
		wantState  []string
	}{
		{"quiet stays quiet", db.AlertRule{ThrottleSeconds: 600}, finding{}, actNone, nil, nil},
		{"first breach fires", db.AlertRule{ThrottleSeconds: 600}, breach("a"), actFire, []string{"a"}, []string{"a"}},
		{"ongoing episode does not re-fire",
			db.AlertRule{ThrottleSeconds: 600, FiringSince: ago(time.Hour), LastFiredAt: ago(time.Hour), FiringTargets: []string{"a"}},
			breach("a"), actNone, nil, nil},
		{"new target joining an episode fires for it alone",
			db.AlertRule{ThrottleSeconds: 600, FiringSince: ago(time.Hour), LastFiredAt: ago(time.Hour), FiringTargets: []string{"a"}},
			breach("a", "b"), actFire, []string{"b"}, []string{"a", "b"}},
		{"target leaving an open episode is recorded, not announced",
			db.AlertRule{ThrottleSeconds: 600, FiringSince: ago(time.Hour), LastFiredAt: ago(time.Hour), FiringTargets: []string{"a", "b"}},
			breach("a"), actUpdate, nil, []string{"a"}},
		// a recovered (and was dropped above) while b kept the episode
		// open; a breaking again must page.
		{"recovered target that re-breaks fires again",
			db.AlertRule{ThrottleSeconds: 600, FiringSince: ago(time.Hour), LastFiredAt: ago(time.Hour), FiringTargets: []string{"b"}},
			breach("a", "b"), actFire, []string{"a"}, []string{"a", "b"}},
		{"clear closes the episode",
			db.AlertRule{ThrottleSeconds: 600, FiringSince: ago(time.Hour), LastFiredAt: ago(time.Hour), FiringTargets: []string{"a"}},
			finding{}, actResolve, nil, nil},
		{"flap inside cooldown is held",
			db.AlertRule{ThrottleSeconds: 600, LastFiredAt: ago(2 * time.Minute)},
			breach("a"), actNone, nil, nil},
		{"breach after cooldown fires again",
			db.AlertRule{ThrottleSeconds: 600, LastFiredAt: ago(11 * time.Minute)},
			breach("a"), actFire, []string{"a"}, []string{"a"}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := decideEpisode(&tc.rule, tc.f, now)
			if d.action != tc.wantAction {
				t.Fatalf("action = %v, want %v", d.action, tc.wantAction)
			}
			if strings.Join(d.newTargets, ",") != strings.Join(tc.wantNew, ",") {
				t.Errorf("newTargets = %v, want %v", d.newTargets, tc.wantNew)
			}
			if (tc.wantAction == actFire || tc.wantAction == actUpdate) && strings.Join(d.targets, ",") != strings.Join(tc.wantState, ",") {
				t.Errorf("targets = %v, want %v", d.targets, tc.wantState)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Fixtures: fake cluster (envs + certs), fake prometheus, fake DNS.
// ---------------------------------------------------------------------------

type envFixture struct {
	ns, project, service, env string
	host                      string
	extra                     []string
	internal                  bool
}

func envObj(f envFixture) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("application.kuso.sislelabs.com/v1alpha1")
	u.SetKind("KusoEnvironment")
	u.SetNamespace(f.ns)
	u.SetName(f.project + "-" + f.service + "-" + f.env)
	u.SetLabels(map[string]string{kube.LabelProject: f.project, kube.LabelService: f.service, kube.LabelEnv: f.env})
	extra := make([]any, 0, len(f.extra))
	for _, h := range f.extra {
		extra = append(extra, h)
	}
	u.Object["spec"] = map[string]any{
		"project": f.project, "service": f.project + "-" + f.service,
		"host": f.host, "additionalHosts": extra, "internal": f.internal,
	}
	return u
}

func certObj(ns, name string, dnsNames []string, notAfter time.Time, ready bool, reason, msg string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("cert-manager.io/v1")
	u.SetKind("Certificate")
	u.SetNamespace(ns)
	u.SetName(name)
	names := make([]any, 0, len(dnsNames))
	for _, n := range dnsNames {
		names = append(names, n)
	}
	status := "True"
	if !ready {
		status = "False"
	}
	u.Object["spec"] = map[string]any{"dnsNames": names}
	u.Object["status"] = map[string]any{
		"notAfter": notAfter.UTC().Format(time.RFC3339),
		"conditions": []any{map[string]any{
			"type": "Ready", "status": status, "reason": reason, "message": msg,
			"lastTransitionTime": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
		}},
	}
	return u
}

func fakeKube(objs ...runtime.Object) *kube.Client {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			kube.GVREnvironments:      "KusoEnvironmentList",
			hostcheck.GVRCertificates: "CertificateList",
		}, objs...)
	cs := kubefake.NewSimpleClientset(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "n1"},
		Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{
			{Type: corev1.NodeExternalIP, Address: "203.0.113.10"},
		}},
	})
	return &kube.Client{Dynamic: dyn, Clientset: cs}
}

// fakeProm answers instant queries by substring. A key is "&&"-joined
// parts that must all appear in the query; the key with the most parts
// wins, so reqKey+"&&"+errKey beats the plain reqKey.
func fakeProm(t *testing.T, answers map[string]map[string]string, queries *[]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query().Get("query")
		if queries != nil {
			*queries = append(*queries, q)
		}
		best, bestParts := "", 0
	keys:
		for k := range answers {
			parts := strings.Split(k, "&&")
			for _, p := range parts {
				if !strings.Contains(q, p) {
					continue keys
				}
			}
			if len(parts) > bestParts {
				best, bestParts = k, len(parts)
			}
		}
		var b strings.Builder
		b.WriteString(`{"status":"success","data":{"resultType":"vector","result":[`)
		first := true
		for svc, v := range answers[best] {
			if !first {
				b.WriteString(",")
			}
			first = false
			b.WriteString(`{"metric":{"service":"` + svc + `"},"value":[1790000000,"` + v + `"]}`)
		}
		b.WriteString(`]}}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(b.String()))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testEngine(k *kube.Client, promURL string) *Engine {
	e := New(nil, nil, k, nil, slogDiscard())
	e.PromURL = promURL
	return e
}

const (
	reqKey   = `traefik_service_requests_total{service=~`
	errKey   = `code=~"5.."`
	p95Key   = `histogram_quantile(0.95`
	webProd  = "kuso-shop-web-production-http@kubernetes"
	webStage = "kuso-shop-web-staging-http@kubernetes"
	apiProd  = "kuso-shop-api-production-http@kubernetes"
)

func shopEnvs() []runtime.Object {
	return []runtime.Object{
		envObj(envFixture{ns: "kuso", project: "shop", service: "web", env: "production", host: "web.shop.example.com"}),
		envObj(envFixture{ns: "kuso", project: "shop", service: "web", env: "staging", host: "staging.shop.example.com"}),
		envObj(envFixture{ns: "kuso", project: "shop", service: "api", env: "production", host: "api.shop.example.com"}),
		envObj(envFixture{ns: "kuso", project: "blog", service: "web", env: "production", host: "blog.example.com"}),
	}
}

// ---------------------------------------------------------------------------
// http_5xx_rate
// ---------------------------------------------------------------------------

func TestEvaluateHTTP5xxRate(t *testing.T) {
	t.Parallel()
	var queries []string
	prom := fakeProm(t, map[string]map[string]string{
		reqKey:                 {webProd: "400", webStage: "10", apiProd: "1000"},
		reqKey + "&&" + errKey: {webProd: "48", webStage: "9", apiProd: "10"},
	}, &queries)
	e := testEngine(fakeKube(shopEnvs()...), prom.URL)
	pct := 5.0
	f, err := e.evaluateEpisodic(context.Background(), &db.AlertRule{
		ID: "r", Kind: db.AlertKindHTTP5xxRate, Project: "shop",
		ThresholdFloat: &pct, ThresholdInt: i64(20), WindowSeconds: 300,
	}, time.Now())
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	// web/production: 12% of 400 → breach. staging: 90% but only 10
	// requests (< min 20) → ignored as noise. api: 1% → fine.
	if strings.Join(f.targets, ",") != "shop-web-production" {
		t.Fatalf("targets = %v, want [shop-web-production]", f.targets)
	}
	d := f.details["shop-web-production"]
	for _, want := range []string{"12.0%", "400", "5%", "5m"} {
		if !strings.Contains(d, want) {
			t.Errorf("detail %q missing %q", d, want)
		}
	}
	// The query is scoped to the in-scope envs' traefik services; the
	// blog project must not be in the matcher.
	for _, q := range queries {
		if strings.Contains(q, "blog") {
			t.Errorf("query leaked an out-of-scope env: %s", q)
		}
		if !strings.Contains(q, "[300s]") {
			t.Errorf("query ignores the rule window: %s", q)
		}
	}
}

func TestEvaluateHTTP5xxRateScopesToServiceAndEnv(t *testing.T) {
	t.Parallel()
	prom := fakeProm(t, map[string]map[string]string{
		reqKey:                 {webProd: "400", webStage: "400", apiProd: "400"},
		reqKey + "&&" + errKey: {webProd: "400", webStage: "400", apiProd: "400"},
	}, nil)
	e := testEngine(fakeKube(shopEnvs()...), prom.URL)
	f, err := e.evaluateEpisodic(context.Background(), &db.AlertRule{
		ID: "r", Kind: db.AlertKindHTTP5xxRate, Project: "shop", Service: "web", Env: "staging",
		ThresholdFloat: f64(5), WindowSeconds: 300,
	}, time.Now())
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if strings.Join(f.targets, ",") != "shop-web-staging" {
		t.Fatalf("targets = %v, want only the scoped env", f.targets)
	}
}

func TestEvaluateHTTPPromDownIsAnError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	e := testEngine(fakeKube(shopEnvs()...), srv.URL)
	_, err := e.evaluateEpisodic(context.Background(), &db.AlertRule{
		ID: "r", Kind: db.AlertKindHTTP5xxRate, Project: "shop", ThresholdFloat: f64(5),
	}, time.Now())
	// An error (not an empty finding) — an empty finding would RESOLVE
	// an open episode just because prometheus blipped.
	if err == nil {
		t.Fatal("prometheus 503 must surface as an error")
	}
}

// ---------------------------------------------------------------------------
// http_p95_latency
// ---------------------------------------------------------------------------

func TestEvaluateHTTPP95Latency(t *testing.T) {
	t.Parallel()
	prom := fakeProm(t, map[string]map[string]string{
		reqKey: {webProd: "500", apiProd: "500", webStage: "3"},
		p95Key: {webProd: "2.5", apiProd: "0.2", webStage: "9"},
	}, nil)
	e := testEngine(fakeKube(shopEnvs()...), prom.URL)
	f, err := e.evaluateEpisodic(context.Background(), &db.AlertRule{
		ID: "r", Kind: db.AlertKindHTTPP95Latency, Project: "shop",
		ThresholdFloat: f64(1000), ThresholdInt: i64(20), WindowSeconds: 600,
	}, time.Now())
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if strings.Join(f.targets, ",") != "shop-web-production" {
		t.Fatalf("targets = %v, want [shop-web-production]", f.targets)
	}
	d := f.details["shop-web-production"]
	for _, want := range []string{"2500ms", "1000ms", "10m"} {
		if !strings.Contains(d, want) {
			t.Errorf("detail %q missing %q", d, want)
		}
	}
}

// ---------------------------------------------------------------------------
// cert_expiry
// ---------------------------------------------------------------------------

func TestEvaluateCertExpiry(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	objs := append(shopEnvs(),
		certObj("kuso", "shop-web-production-tls", []string{"web.shop.example.com"}, now.Add(5*24*time.Hour), true, "Ready", ""),
		certObj("kuso", "shop-api-production-tls", []string{"api.shop.example.com"}, now.Add(60*24*time.Hour), false, "Failed", "ACME order failed"),
		certObj("kuso", "shop-web-staging-tls", []string{"staging.shop.example.com"}, now.Add(60*24*time.Hour), true, "Ready", ""),
		certObj("kuso", "blog-web-production-tls", []string{"blog.example.com"}, now.Add(24*time.Hour), true, "Ready", ""),
		certObj("kuso", "kuso-ui-tls", []string{"kuso.example.com"}, now.Add(2*24*time.Hour), true, "Ready", ""),
	)
	e := testEngine(fakeKube(objs...), "")

	// Instance-wide: every failing cert, including ones no env owns
	// (the kuso UI's own cert).
	f, err := e.evaluateEpisodic(context.Background(), &db.AlertRule{ID: "r", Kind: db.AlertKindCertExpiry, ThresholdInt: i64(14)}, now)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	want := "cert:kuso/blog-web-production-tls,cert:kuso/kuso-ui-tls,cert:kuso/shop-api-production-tls,cert:kuso/shop-web-production-tls"
	if strings.Join(f.targets, ",") != want {
		t.Fatalf("targets = %v\nwant %s", f.targets, want)
	}
	if d := f.details["cert:kuso/shop-api-production-tls"]; !strings.Contains(d, "not Ready (Failed: ACME order failed)") || !strings.Contains(d, "api.shop.example.com") {
		t.Errorf("not-ready detail = %q", d)
	}
	if d := f.details["cert:kuso/shop-web-production-tls"]; !strings.Contains(d, "expires in") || !strings.Contains(d, "shop / web → production") {
		t.Errorf("expiry detail = %q", d)
	}

	// Project-scoped: only certs serving that project's env hosts.
	f, err = e.evaluateEpisodic(context.Background(), &db.AlertRule{ID: "r", Kind: db.AlertKindCertExpiry, Project: "shop", ThresholdInt: i64(14)}, now)
	if err != nil {
		t.Fatalf("evaluate scoped: %v", err)
	}
	if got := strings.Join(f.targets, ","); got != "cert:kuso/shop-api-production-tls,cert:kuso/shop-web-production-tls" {
		t.Errorf("scoped targets = %s", got)
	}
}

func TestEvaluateCertExpiryWithoutCertManager(t *testing.T) {
	t.Parallel()
	// A cluster without the cert-manager CRD answers the LIST with 404.
	// That's "no certs", not a broken rule.
	k := fakeKube(shopEnvs()...)
	k.Dynamic.(*dynamicfake.FakeDynamicClient).PrependReactor("list", "certificates",
		func(action k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: "cert-manager.io", Resource: "certificates"}, "")
		})
	e := testEngine(k, "")
	f, err := e.evaluateEpisodic(context.Background(), &db.AlertRule{ID: "r", Kind: db.AlertKindCertExpiry, ThresholdInt: i64(14)}, time.Now())
	if err != nil || len(f.targets) != 0 {
		t.Fatalf("no cert-manager: err=%v targets=%v, want neither", err, f.targets)
	}
}

// ---------------------------------------------------------------------------
// dns_mismatch
// ---------------------------------------------------------------------------

type mapResolver map[string][]string

func (m mapResolver) LookupHost(ctx context.Context, host string) ([]string, error) {
	if v, ok := m[host]; ok {
		return v, nil
	}
	return nil, errors.New("no such host")
}

func TestEvaluateDNSMismatch(t *testing.T) {
	t.Parallel()
	objs := []runtime.Object{
		envObj(envFixture{ns: "kuso", project: "shop", service: "web", env: "production",
			host: "web.shop.example.com", extra: []string{"shop.com", "*.shop.com", "old.shop.com"}}),
		envObj(envFixture{ns: "kuso", project: "shop", service: "db", env: "production", host: "db.shop.example.com", internal: true}),
		envObj(envFixture{ns: "kuso", project: "blog", service: "web", env: "production", host: "blog.example.com"}),
	}
	e := testEngine(fakeKube(objs...), "")
	e.Resolver = mapResolver{
		"web.shop.example.com": {"203.0.113.10"},
		"shop.com":             {"198.51.100.99"},
		"blog.example.com":     {"198.51.100.1"},
	}
	f, err := e.evaluateEpisodic(context.Background(), &db.AlertRule{ID: "r", Kind: db.AlertKindDNSMismatch, Project: "shop"}, time.Now())
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := strings.Join(f.targets, ","); got != "dns:old.shop.com,dns:shop.com" {
		t.Fatalf("targets = %s, want old.shop.com (unresolved) + shop.com (wrong IP)", got)
	}
	if d := f.details["dns:shop.com"]; !strings.Contains(d, "198.51.100.99") || !strings.Contains(d, "203.0.113.10") {
		t.Errorf("mismatch detail should show resolved vs expected: %q", d)
	}
	if d := f.details["dns:old.shop.com"]; !strings.Contains(d, "does not resolve") {
		t.Errorf("unresolved detail = %q", d)
	}
}

func TestEvaluateDNSNoIngressIPsIsAnError(t *testing.T) {
	t.Parallel()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{kube.GVREnvironments: "KusoEnvironmentList"},
		envObj(envFixture{ns: "kuso", project: "shop", service: "web", env: "production", host: "web.shop.example.com"}))
	e := testEngine(&kube.Client{Dynamic: dyn, Clientset: kubefake.NewSimpleClientset()}, "")
	e.Resolver = mapResolver{"web.shop.example.com": {"203.0.113.10"}}
	// Without any known ingress IP every host would "mismatch" — refuse
	// to evaluate rather than page on every domain.
	if _, err := e.evaluateEpisodic(context.Background(), &db.AlertRule{ID: "r", Kind: db.AlertKindDNSMismatch}, time.Now()); err == nil {
		t.Fatal("expected an error when no ingress IPs are known")
	}
}

// ---------------------------------------------------------------------------
// Resolution card
// ---------------------------------------------------------------------------

func TestResolvedEvent(t *testing.T) {
	t.Parallel()
	since := time.Now().Add(-14 * time.Minute)
	r := &db.AlertRule{ID: "r1", Name: "5xx rate", Kind: db.AlertKindHTTP5xxRate, Project: "shop", Service: "web", Severity: "error", FiringSince: &since}
	ev := resolvedEvent(r, time.Now())
	if ev.Type != notify.EventAlertFired || ev.Severity != "info" {
		t.Errorf("type/severity = %s/%s", ev.Type, ev.Severity)
	}
	if !strings.HasPrefix(ev.Title, "✓ Resolved · 5xx rate") || !strings.Contains(ev.Title, "shop / web") {
		t.Errorf("title = %q", ev.Title)
	}
	if ev.Extra["state"] != "resolved" || ev.Extra["rule_id"] != "r1" {
		t.Errorf("extra = %v", ev.Extra)
	}
	if !strings.Contains(ev.Body, "14m") {
		t.Errorf("body should say how long it lasted: %q", ev.Body)
	}
}

// evalEpisode must not touch the DB or notify when the evaluator errors.
func TestEvalEpisodeErrorIsInert(t *testing.T) {
	t.Parallel()
	e := &Engine{Logger: slogDiscard()}
	var called atomic.Int32
	e.episodicFn = func(ctx context.Context, r *db.AlertRule, now time.Time) (finding, error) {
		called.Add(1)
		return finding{}, errors.New("prometheus down")
	}
	since := time.Now().Add(-time.Hour)
	// DB and Notify are nil: any attempt to resolve would panic.
	e.evalOne(context.Background(), &db.AlertRule{ID: "r", Kind: db.AlertKindHTTP5xxRate, FiringSince: &since}, time.Now())
	if called.Load() != 1 {
		t.Fatalf("episodic evaluator called %d times", called.Load())
	}
}
