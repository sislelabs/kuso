package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	"kuso/server/internal/db"
	"kuso/server/internal/kube"
)

// ErrorsHandler exposes /api/projects/{project}/services/{service}/errors.
// Reads the ErrorEvent table populated by the errorscan goroutine and
// returns groups (one row per fingerprint).
type ErrorsHandler struct {
	DB     *db.DB
	Logger *slog.Logger
	// Kube + Namespace let List 404 an unknown project/service. Optional.
	Kube      *kube.Client
	Namespace string
}

// Mount registers the route. JWT-protected — the caller's project
// access is verified against requireProjectAccess.
func (h *ErrorsHandler) Mount(r chi.Router) {
	r.Get("/api/projects/{project}/services/{service}/errors", h.List)
	r.Get("/api/projects/{project}/services/{service}/errors/{fingerprint}", h.Detail)
	r.Post("/api/projects/{project}/services/{service}/errors/{fingerprint}/resolve", h.Resolve)
	r.Delete("/api/projects/{project}/services/{service}/errors/{fingerprint}/resolve", h.Resolve)
	r.Get("/api/projects/{project}/errors/summary", h.Summary)
}

// errorSummaryResponse keys services by SHORT name (the canvas's key).
type errorSummaryResponse struct {
	Since    time.Time                              `json:"since"`
	Services map[string]errorSummaryServiceResponse `json:"services"`
}

type errorSummaryServiceResponse struct {
	Occurrences int64     `json:"occurrences"`
	Groups      int64     `json:"groups"`
	LastSeen    time.Time `json:"lastSeen"`
}

// Summary returns per-service error counts for a project in one request
// (the canvas badges). ?since= and ?env= as in List; default 24h.
// Services with no errors in the window are absent from the map.
func (h *ErrorsHandler) Summary(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	project := chi.URLParam(r, "project")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleViewer) {
		return
	}
	if !requireParent(ctx, w, h.Kube, h.Namespace, project, "") {
		return
	}
	window := 24 * time.Hour
	if v := r.URL.Query().Get("since"); v != "" {
		d, err := parseRangeDuration(v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("invalid since %q: use a Go duration (30m, 6h) or days/weeks (7d, 2w), max 30d", v))
			return
		}
		window = min(d, 30*24*time.Hour)
	}
	since := time.Now().Add(-window)
	live := projectServiceNames(ctx, h.Kube, h.Namespace, project)
	rows, err := h.DB.SummarizeProjectErrors(ctx, project, r.URL.Query().Get("env"), since, live)
	if err != nil {
		h.Logger.Error("errors: summary", "err", err, "project", project)
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	out := errorSummaryResponse{Since: since.UTC(), Services: make(map[string]errorSummaryServiceResponse, len(rows))}
	for _, s := range rows {
		// A deleted service's errors linger until retention prunes them;
		// counted here they made a project's total exceed the sum of its
		// services' badges.
		born, isLive := live[s.Service]
		if live != nil && !isLive {
			continue
		}
		if err := h.discountResolved(ctx, project, r.URL.Query().Get("env"), laterOf(since, born), &s); err != nil {
			h.Logger.Warn("errors: summary resolved marks", "err", err, "project", project, "service", s.Service)
		}
		if s.Groups == 0 {
			continue
		}
		out.Services[strings.TrimPrefix(s.Service, project+"-")] = errorSummaryServiceResponse{
			Occurrences: s.Occurrences,
			Groups:      s.Groups,
			LastSeen:    s.LastSeen,
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// List returns aggregated error groups. Query params:
//
//	?since=24h   — lookback window. Default 24h, max 30d.
//	?limit=50    — max groups returned (1–200, default 50).
//	?offset=0    — groups to skip (offset paging, newest-first order).
//	?env=staging — only that environment's errors (default: all).
//	?resolved=include — also list resolved groups that haven't recurred.
//
// Truncation signal (agent-use W4): the wire shape is a bare JSON
// array and MUST stay that way, so a cut response is signalled via
// headers instead — X-Kuso-Truncated: true plus X-Kuso-Next-Offset
// with the ?offset= value for the next page. Detection is exact (the
// DB read over-fetches one group past the limit), not a full-page
// guess. Absent headers mean the response is complete.
func (h *ErrorsHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	project := chi.URLParam(r, "project")
	service := chi.URLParam(r, "service")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleViewer) {
		return
	}
	if !requireParent(ctx, w, h.Kube, h.Namespace, project, service) {
		return
	}
	// ErrorEvent.service is copied from LogLine.service, which is the FQ
	// `<project>-<service>` form; callers pass the short name. Same
	// widening as LogSearchHandler.Search.
	if !strings.HasPrefix(service, project+"-") {
		service = project + "-" + service
	}
	// A bad `since` is rejected rather than silently defaulted. The old
	// code swallowed the parse error, so the UI's "7d"/"30d" options —
	// which time.ParseDuration cannot parse — quietly queried 24h while
	// the panel still rendered "0 error groups in last 7d".
	since := time.Now().Add(-24 * time.Hour)
	if v := r.URL.Query().Get("since"); v != "" {
		d, err := parseRangeDuration(v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("invalid since %q: use a Go duration (30m, 6h) or days/weeks (7d, 2w), max 30d", v))
			return
		}
		if d > 30*24*time.Hour {
			d = 30 * 24 * time.Hour
		}
		since = time.Now().Add(-d)
	}
	since = laterOf(since, serviceBornAt(ctx, h.Kube, h.Namespace, project, service))
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	offset := 0
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			offset = n
		}
	}
	// +1 over-fetch: one extra group past the limit proves truncation.
	groups, err := h.DB.ListErrorGroups(ctx, project, service, r.URL.Query().Get("env"), since, limit+1, offset)
	if err != nil {
		h.Logger.Error("errors: list", "err", err, "project", project, "service", service)
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	if len(groups) > limit {
		groups = groups[:limit]
		setTruncationHeaders(w, headerNextOffset, strconv.Itoa(offset+limit))
	}
	// Resolved groups stay hidden until they recur; ?resolved=include
	// lists them too (each carries resolvedAt).
	marks, err := h.loadResolved(ctx, project, service)
	if err != nil {
		h.Logger.Warn("errors: resolved marks", "err", err, "project", project, "service", service)
	}
	groups = applyResolved(groups, marks, r.URL.Query().Get("resolved") == "include")
	writeJSON(w, http.StatusOK, groups)
}

// Detail returns one fingerprint's count histogram + newest occurrences.
// Same ?since= / ?env= semantics as List. The bucket width targets ~48
// bars across the window, never below a minute.
func (h *ErrorsHandler) Detail(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	project := chi.URLParam(r, "project")
	service := chi.URLParam(r, "service")
	fingerprint := chi.URLParam(r, "fingerprint")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleViewer) {
		return
	}
	if !requireParent(ctx, w, h.Kube, h.Namespace, project, service) {
		return
	}
	if !strings.HasPrefix(service, project+"-") {
		service = project + "-" + service
	}
	if fingerprint == "" || len(fingerprint) > 256 {
		writeErr(w, http.StatusBadRequest, "invalid fingerprint")
		return
	}
	window := 24 * time.Hour
	if v := r.URL.Query().Get("since"); v != "" {
		d, err := parseRangeDuration(v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("invalid since %q: use a Go duration (30m, 6h) or days/weeks (7d, 2w), max 30d", v))
			return
		}
		if d > 30*24*time.Hour {
			d = 30 * 24 * time.Hour
		}
		window = d
	}
	detail, err := h.DB.GetErrorGroupDetail(ctx, project, service, r.URL.Query().Get("env"), fingerprint,
		laterOf(time.Now().Add(-window), serviceBornAt(ctx, h.Kube, h.Namespace, project, service)), window/48, 20)
	if err != nil {
		h.Logger.Error("errors: detail", "err", err, "project", project, "service", service)
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

// projectServiceNames maps the FQ names of the project's KusoServices to
// their CR creation time, or returns nil when they can't be listed (no
// kube client, transient error); the caller then filters nothing.
//
// The creation time is the service's identity for history: ErrorEvent,
// Revision and Audit rows are keyed by name only, so a service re-created
// under a deleted one's name would otherwise inherit its records.
func projectServiceNames(ctx context.Context, kc *kube.Client, homeNS, project string) map[string]time.Time {
	if kc == nil || kc.Dynamic == nil {
		return nil
	}
	ns := projectNamespace(ctx, kc, homeNS, project)
	if ns == "" {
		return nil
	}
	var items []*unstructured.Unstructured
	cached := false
	if kc.Cache != nil {
		// Read-only (names + timestamps): skip the per-object DeepCopy.
		items, cached = kc.Cache.ListFromCacheShared(kube.GVRServices, ns, labels.Everything())
	}
	if !cached {
		list, err := kc.Dynamic.Resource(kube.GVRServices).Namespace(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil
		}
		for i := range list.Items {
			items = append(items, &list.Items[i])
		}
	}
	out := make(map[string]time.Time, len(items))
	prefix := project + "-"
	for _, u := range items {
		if strings.HasPrefix(u.GetName(), prefix) {
			out[u.GetName()] = u.GetCreationTimestamp().Time
		}
	}
	return out
}

// serviceBornAt is the live service CR's creation time, or zero when it
// can't be read. Reads of per-service history clamp their window to it.
func serviceBornAt(ctx context.Context, kc *kube.Client, homeNS, project, service string) time.Time {
	if kc == nil || kc.Dynamic == nil {
		return time.Time{}
	}
	ns := projectNamespace(ctx, kc, homeNS, project)
	if ns == "" {
		return time.Time{}
	}
	u, err := lookupCR(ctx, kc, kube.GVRServices, ns, project+"-"+strings.TrimPrefix(service, project+"-"))
	if err != nil {
		return time.Time{}
	}
	return u.GetCreationTimestamp().Time
}

// envServiceBornAt is serviceBornAt for the service owning env CR
// <project>-<env>; zero when the env or its service can't be read.
func envServiceBornAt(ctx context.Context, kc *kube.Client, homeNS, project, env string) time.Time {
	if kc == nil || kc.Dynamic == nil {
		return time.Time{}
	}
	ns := projectNamespace(ctx, kc, homeNS, project)
	if ns == "" {
		return time.Time{}
	}
	u, err := lookupCR(ctx, kc, kube.GVREnvironments, ns, project+"-"+strings.TrimPrefix(env, project+"-"))
	if err != nil {
		return time.Time{}
	}
	service := u.GetLabels()[kube.LabelService]
	if service == "" {
		service, _, _ = unstructured.NestedString(u.Object, "spec", "service")
	}
	if service == "" {
		return time.Time{}
	}
	return serviceBornAt(ctx, kc, homeNS, project, service)
}

// projectNamespace resolves the namespace a project's CRs live in, or ""
// when the project can't be read.
func projectNamespace(ctx context.Context, kc *kube.Client, homeNS, project string) string {
	if homeNS == "" {
		homeNS = "kuso"
	}
	p, err := lookupCR(ctx, kc, kube.GVRProjects, homeNS, project)
	if err != nil {
		return ""
	}
	if v, _, _ := unstructured.NestedString(p.Object, "spec", "namespace"); v != "" {
		return v
	}
	return homeNS
}

func laterOf(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
