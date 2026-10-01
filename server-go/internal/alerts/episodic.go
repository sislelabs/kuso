package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"kuso/server/internal/db"
	"kuso/server/internal/hostcheck"
	"kuso/server/internal/kube"
	"kuso/server/internal/notify"
)

// Episodic rules (http_5xx_rate, http_p95_latency, cert_expiry,
// dns_mismatch) fire once when a condition starts and send a resolution
// when it clears, instead of re-firing every throttle window. A rule can
// cover many things at once (every env of a project, every cert in the
// cluster), so an evaluation yields the set of breaching targets; a new
// target joining an open episode pages for itself alone.

const defaultPromURL = "http://kuso-prometheus.kuso.svc.cluster.local:9090"

// dnsLookupTimeout bounds one host lookup; a hung resolver must not
// eat the rule's whole evaluation budget.
const dnsLookupTimeout = 3 * time.Second

// Defaults when a rule leaves a threshold unset (API/CLI apply the same).
const (
	default5xxPct      = 5.0
	defaultP95Ms       = 1000.0
	defaultMinRequests = 20
	defaultCertDays    = 14
)

// finding is one evaluation: the breaching targets (sorted, stable keys
// such as an env name or "cert:<ns>/<name>") and a human line for each.
type finding struct {
	targets []string
	details map[string]string
}

func (f *finding) add(key, detail string) {
	if f.details == nil {
		f.details = map[string]string{}
	}
	if _, dup := f.details[key]; !dup {
		f.targets = append(f.targets, key)
	}
	f.details[key] = detail
}

type episodeAction int

const (
	actNone episodeAction = iota
	actFire
	actResolve
	// actUpdate persists a shrunken target set without notifying: a
	// target that recovered while others keep the episode open.
	actUpdate
)

type episodeDecision struct {
	action episodeAction
	// newTargets are the targets this fire is about.
	newTargets []string
	// targets is the episode's target set to persist after a fire.
	targets []string
}

// decideEpisode is the episode state machine. ThrottleSeconds acts as a
// flap cooldown: no fire within it of the last fire, so a condition
// bouncing across its threshold doesn't page every minute. Held targets
// aren't recorded, so they fire once the cooldown passes.
func decideEpisode(r *db.AlertRule, f finding, now time.Time) episodeDecision {
	if len(f.targets) == 0 {
		if r.FiringSince != nil {
			return episodeDecision{action: actResolve}
		}
		return episodeDecision{action: actNone}
	}
	known := make(map[string]struct{}, len(r.FiringTargets))
	for _, t := range r.FiringTargets {
		known[t] = struct{}{}
	}
	// still: previously fired and still breaching. A recovered target is
	// dropped, so if it breaks again while the episode stays open it is
	// fresh and pages again.
	var fresh, still []string
	for _, t := range f.targets {
		if _, ok := known[t]; ok {
			still = append(still, t)
		} else {
			fresh = append(fresh, t)
		}
	}
	sort.Strings(still)
	shrunk := len(still) != len(r.FiringTargets)
	if len(fresh) == 0 || (r.LastFiredAt != nil && now.Sub(*r.LastFiredAt) < time.Duration(r.ThrottleSeconds)*time.Second) {
		if shrunk && r.FiringSince != nil {
			return episodeDecision{action: actUpdate, targets: still}
		}
		return episodeDecision{action: actNone}
	}
	targets := append(still, fresh...)
	sort.Strings(targets)
	return episodeDecision{action: actFire, newTargets: fresh, targets: targets}
}

// evalEpisode evaluates an episodic rule and applies the decision:
// notify + persist the episode state. An evaluation error changes
// nothing — a prometheus or apiserver blip must neither fire nor
// resolve.
func (e *Engine) evalEpisode(ctx context.Context, r *db.AlertRule, now time.Time) {
	fn := e.episodicFn
	if fn == nil {
		fn = e.evaluateEpisodic
	}
	f, err := fn(ctx, r, now)
	if err != nil {
		e.Logger.Warn("alert evaluate", "rule", r.Name, "err", err)
		return
	}
	d := decideEpisode(r, f, now)
	switch d.action {
	case actFire:
		e.Notify.Emit(alertEvent(r, fireBody(f, d.newTargets, len(d.targets))))
		since := now
		if r.FiringSince != nil {
			since = *r.FiringSince
		}
		if err := e.DB.SetAlertEpisode(ctx, r.ID, &since, d.targets, &now); err != nil {
			e.Logger.Warn("alert episode stamp failed — may re-fire next tick", "rule", r.Name, "err", err)
		}
	case actUpdate:
		if err := e.DB.SetAlertEpisode(ctx, r.ID, r.FiringSince, d.targets, nil); err != nil {
			e.Logger.Warn("alert episode update failed", "rule", r.Name, "err", err)
		}
	case actResolve:
		e.Notify.Emit(resolvedEvent(r, now))
		if err := e.DB.SetAlertEpisode(ctx, r.ID, nil, nil, nil); err != nil {
			e.Logger.Warn("alert episode clear failed — may resolve again next tick", "rule", r.Name, "err", err)
		}
	}
}

func fireBody(f finding, fresh []string, total int) string {
	lines := make([]string, 0, len(fresh)+1)
	for _, t := range fresh {
		lines = append(lines, f.details[t])
	}
	if older := total - len(fresh); older > 0 {
		lines = append(lines, fmt.Sprintf("(%d more already firing)", older))
	}
	return strings.Join(lines, "\n")
}

func resolvedEvent(r *db.AlertRule, now time.Time) notify.Event {
	body := "Back to normal"
	if r.FiringSince != nil {
		body += " after " + shortDuration(now.Sub(*r.FiringSince))
	}
	if len(r.FiringTargets) > 0 {
		body += " — was firing for " + strings.Join(r.FiringTargets, ", ")
	}
	extra := map[string]string{"rule_id": r.ID, "kind": r.Kind}
	if r.Project != "" {
		extra["project"] = r.Project
	}
	if r.Service != "" {
		extra["service"] = r.Service
	}
	return notify.AlertResolved(r.Name, body, extra)
}

func shortDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// evaluateEpisodic dispatches an episodic rule to its evaluator.
func (e *Engine) evaluateEpisodic(ctx context.Context, r *db.AlertRule, now time.Time) (finding, error) {
	if e.Kube == nil || e.Kube.Dynamic == nil {
		return finding{}, nil
	}
	switch r.Kind {
	case db.AlertKindHTTP5xxRate:
		return e.evalHTTP5xx(ctx, r)
	case db.AlertKindHTTPP95Latency:
		return e.evalHTTPP95(ctx, r)
	case db.AlertKindCertExpiry:
		return e.evalCerts(ctx, r, now)
	case db.AlertKindDNSMismatch:
		return e.evalDNS(ctx, r)
	}
	return finding{}, fmt.Errorf("unknown alert kind: %s", r.Kind)
}

// envTarget is a KusoEnvironment reduced to what the rules scope on.
type envTarget struct {
	name, namespace       string
	project, service, env string
	hosts                 []string
	internal              bool
}

func (t envTarget) scope() string { return notify.Scope(t.project, t.service, t.env) }

func toEnvTarget(k *kube.KusoEnvironment) envTarget {
	t := envTarget{
		name: k.Name, namespace: k.Namespace,
		project:  k.Spec.Project,
		service:  k.Labels[kube.LabelService],
		env:      k.Labels[kube.LabelEnv],
		internal: k.Spec.Internal,
	}
	if t.project == "" {
		t.project = k.Labels[kube.LabelProject]
	}
	if t.service == "" {
		t.service = strings.TrimPrefix(k.Spec.Service, t.project+"-")
	}
	seen := map[string]struct{}{}
	for _, h := range append([]string{k.Spec.Host}, k.Spec.AdditionalHosts...) {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		if _, dup := seen[h]; dup {
			continue
		}
		seen[h] = struct{}{}
		t.hosts = append(t.hosts, h)
	}
	return t
}

// inScope applies the rule's project/service/env scoping. Service may be
// stored short ("web") or fully-qualified ("shop-web"); env matches the
// env label ("production") or the full CR name.
func inScope(r *db.AlertRule, t envTarget) bool {
	if r.Project != "" && t.project != r.Project {
		return false
	}
	if svc := strings.TrimPrefix(r.Service, r.Project+"-"); svc != "" && t.service != svc {
		return false
	}
	if r.Env != "" && t.env != r.Env && t.name != r.Env {
		return false
	}
	return true
}

func (e *Engine) listEnvs(ctx context.Context) ([]envTarget, error) {
	envs, err := e.Kube.ListKusoEnvironments(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("list environments: %w", err)
	}
	out := make([]envTarget, 0, len(envs))
	for i := range envs {
		out = append(out, toEnvTarget(&envs[i]))
	}
	return out, nil
}

func (e *Engine) scopedEnvs(ctx context.Context, r *db.AlertRule) ([]envTarget, error) {
	all, err := e.listEnvs(ctx)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, t := range all {
		if inScope(r, t) {
			out = append(out, t)
		}
	}
	return out, nil
}

func ruleWindow(r *db.AlertRule) time.Duration {
	if r.WindowSeconds <= 0 {
		return 5 * time.Minute
	}
	return time.Duration(r.WindowSeconds) * time.Second
}

func thresholdFloat(r *db.AlertRule, def float64) float64 {
	if r.ThresholdFloat != nil {
		return *r.ThresholdFloat
	}
	return def
}

func thresholdInt(r *db.AlertRule, def int64) int64 {
	if r.ThresholdInt != nil {
		return *r.ThresholdInt
	}
	return def
}

// ---- http_5xx_rate / http_p95_latency --------------------------------

// traefikMatcher builds a service=~ matcher covering the envs' traefik
// services. Traefik labels them "<namespace>-<k8s service>-<port>@kubernetes"
// and the k8s Service is named after the env CR.
func traefikMatcher(envs []envTarget) string {
	alts := make([]string, 0, len(envs))
	for _, t := range envs {
		alts = append(alts, regexp.QuoteMeta(t.namespace+"-"+t.name))
	}
	sort.Strings(alts)
	return promLabelEscape("(" + strings.Join(alts, "|") + ")-.*@kubernetes")
}

func promLabelEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}

// envForService maps a traefik service label back to its env: the
// longest "<ns>-<env>-" prefix wins, so "shop-web-production-2" doesn't
// swallow "shop-web-production"'s traffic or vice versa.
func envForService(envs []envTarget, label string) (envTarget, bool) {
	var best envTarget
	found := false
	for _, t := range envs {
		p := t.namespace + "-" + t.name + "-"
		if strings.HasPrefix(label, p) && (!found || len(p) > len(best.namespace)+len(best.name)+2) {
			best, found = t, true
		}
	}
	return best, found
}

// perEnv sums a by-service vector into per-env totals. NaN samples
// (histogram_quantile over zero traffic) are skipped.
func perEnv(envs []envTarget, vec map[string]float64, combine func(a, b float64) float64) map[string]float64 {
	out := map[string]float64{}
	for label, v := range vec {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		t, ok := envForService(envs, label)
		if !ok {
			continue
		}
		if cur, seen := out[t.name]; seen {
			out[t.name] = combine(cur, v)
		} else {
			out[t.name] = v
		}
	}
	return out
}

func sum(a, b float64) float64 { return a + b }

func (e *Engine) evalHTTP5xx(ctx context.Context, r *db.AlertRule) (finding, error) {
	envs, err := e.scopedEnvs(ctx, r)
	if err != nil || len(envs) == 0 {
		return finding{}, err
	}
	win := ruleWindow(r)
	m := traefikMatcher(envs)
	secs := int(win.Seconds())
	totalVec, err := e.promVector(ctx, fmt.Sprintf(
		`sum by (service) (increase(traefik_service_requests_total{service=~"%s"}[%ds]))`, m, secs))
	if err != nil {
		return finding{}, err
	}
	badVec, err := e.promVector(ctx, fmt.Sprintf(
		`sum by (service) (increase(traefik_service_requests_total{service=~"%s",code=~"5.."}[%ds]))`, m, secs))
	if err != nil {
		return finding{}, err
	}
	total, bad := perEnv(envs, totalVec, sum), perEnv(envs, badVec, sum)
	threshold := thresholdFloat(r, default5xxPct)
	minReq := float64(thresholdInt(r, defaultMinRequests))
	var f finding
	for _, t := range envs {
		n := total[t.name]
		if n < minReq || n <= 0 {
			continue
		}
		pct := bad[t.name] / n * 100
		if pct >= threshold {
			f.add(t.name, fmt.Sprintf("%s: %.1f%% of %.0f requests returned 5xx over %s (threshold %g%%)",
				t.scope(), pct, n, shortDuration(win), threshold))
		}
	}
	sort.Strings(f.targets)
	return f, nil
}

func (e *Engine) evalHTTPP95(ctx context.Context, r *db.AlertRule) (finding, error) {
	envs, err := e.scopedEnvs(ctx, r)
	if err != nil || len(envs) == 0 {
		return finding{}, err
	}
	win := ruleWindow(r)
	m := traefikMatcher(envs)
	secs := int(win.Seconds())
	totalVec, err := e.promVector(ctx, fmt.Sprintf(
		`sum by (service) (increase(traefik_service_requests_total{service=~"%s"}[%ds]))`, m, secs))
	if err != nil {
		return finding{}, err
	}
	p95Vec, err := e.promVector(ctx, fmt.Sprintf(
		`histogram_quantile(0.95, sum by (service, le) (rate(traefik_service_request_duration_seconds_bucket{service=~"%s"}[%ds])))`, m, secs))
	if err != nil {
		return finding{}, err
	}
	total, p95 := perEnv(envs, totalVec, sum), perEnv(envs, p95Vec, math.Max)
	thresholdMs := thresholdFloat(r, defaultP95Ms)
	minReq := float64(thresholdInt(r, defaultMinRequests))
	var f finding
	for _, t := range envs {
		sec, ok := p95[t.name]
		if !ok || total[t.name] < minReq || total[t.name] <= 0 {
			continue
		}
		if ms := sec * 1000; ms >= thresholdMs {
			f.add(t.name, fmt.Sprintf("%s: p95 latency %.0fms over %s across %.0f requests (threshold %.0fms)",
				t.scope(), ms, shortDuration(win), total[t.name], thresholdMs))
		}
	}
	sort.Strings(f.targets)
	return f, nil
}

// promVector runs an instant query and returns value by "service" label.
func (e *Engine) promVector(ctx context.Context, query string) (map[string]float64, error) {
	base := e.PromURL
	if base == "" {
		base = defaultPromURL
	}
	c := e.httpc
	if c == nil {
		c = &http.Client{Timeout: 5 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/query?"+url.Values{"query": {query}}.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("prometheus: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prometheus: status %d", resp.StatusCode)
	}
	var body struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  [2]any            `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("prometheus: decode: %w", err)
	}
	if body.Status != "success" {
		return nil, fmt.Errorf("prometheus: %s", body.Error)
	}
	out := make(map[string]float64, len(body.Data.Result))
	for _, s := range body.Data.Result {
		str, ok := s.Value[1].(string)
		if !ok {
			continue
		}
		v, err := strconv.ParseFloat(str, 64)
		if err != nil {
			continue
		}
		out[s.Metric["service"]] = v
	}
	return out, nil
}

// ---- cert_expiry -------------------------------------------------------

func (e *Engine) evalCerts(ctx context.Context, r *db.AlertRule, now time.Time) (finding, error) {
	certs, err := hostcheck.ListCertificates(ctx, e.Kube.Dynamic)
	if err != nil {
		// No cert-manager CRD → nothing to watch, not a broken rule.
		if apierrors.IsNotFound(err) {
			return finding{}, nil
		}
		return finding{}, err
	}
	envs, err := e.listEnvs(ctx)
	if err != nil {
		return finding{}, err
	}
	byHost := map[string]envTarget{}
	for _, t := range envs {
		for _, h := range t.hosts {
			byHost[h] = t
		}
	}
	scoped := r.Project != "" || r.Service != "" || r.Env != ""
	days := int(thresholdInt(r, defaultCertDays))
	var f finding
	for _, c := range certs {
		problem, bad := hostcheck.CertProblem(c, now, days)
		owner, owned := envTarget{}, false
		for _, n := range c.DNSNames {
			if t, ok := byHost[strings.ToLower(n)]; ok {
				owner, owned = t, true
				break
			}
		}
		if scoped && (!owned || !inScope(r, owner)) {
			continue
		}
		if !bad {
			continue
		}
		where := strings.Join(c.DNSNames, ", ")
		if owned {
			where += " (" + owner.scope() + ")"
		}
		f.add("cert:"+c.Namespace+"/"+c.Name, fmt.Sprintf("%s: certificate %s", where, problem))
	}
	sort.Strings(f.targets)
	return f, nil
}

// ---- dns_mismatch ------------------------------------------------------

func (e *Engine) evalDNS(ctx context.Context, r *db.AlertRule) (finding, error) {
	if e.Kube.Clientset == nil || e.Resolver == nil {
		return finding{}, nil
	}
	envs, err := e.scopedEnvs(ctx, r)
	if err != nil || len(envs) == 0 {
		return finding{}, err
	}
	expected, err := hostcheck.ExpectedIngressIPs(ctx, e.Kube.Clientset, e.IngressIPs)
	if err != nil {
		return finding{}, err
	}
	if len(expected) == 0 {
		return finding{}, errors.New("no ingress IPs known (no traefik LoadBalancer address or node IPs); set KUSO_INGRESS_IPS")
	}
	hostEnv := map[string]envTarget{}
	var hosts []string
	for _, t := range envs {
		if t.internal {
			continue
		}
		for _, h := range t.hosts {
			if _, dup := hostEnv[h]; dup || !hostcheck.Checkable(h) {
				continue
			}
			hostEnv[h] = t
			hosts = append(hosts, h)
		}
	}
	var f finding
	for _, res := range hostcheck.CheckHosts(ctx, e.Resolver, hosts, expected, dnsLookupTimeout) {
		scope := hostEnv[res.Host].scope()
		switch res.Status {
		case hostcheck.DNSMismatch:
			f.add("dns:"+res.Host, fmt.Sprintf("%s (%s) resolves to %s, expected one of %s",
				res.Host, scope, strings.Join(res.Resolved, ", "), strings.Join(expected, ", ")))
		case hostcheck.DNSUnresolved:
			reason := "no records"
			if res.Err != nil {
				reason = res.Err.Error()
			}
			f.add("dns:"+res.Host, fmt.Sprintf("%s (%s) does not resolve: %s", res.Host, scope, reason))
		}
	}
	sort.Strings(f.targets)
	return f, nil
}
