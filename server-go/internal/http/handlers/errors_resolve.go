package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/db"
)

// Resolved error groups live in one Setting row per service (no schema
// change): fingerprint → when it was resolved. A resolved group is hidden
// until an occurrence newer than that time arrives, then it shows again as
// a recurrence.

// resolvedRetention drops marks older than the longest error window.
const resolvedRetention = 30 * 24 * time.Hour

func resolvedKey(project, fqService string) string {
	return "errors.resolved:" + project + ":" + fqService
}

func (h *ErrorsHandler) loadResolved(ctx context.Context, project, fqService string) (map[string]time.Time, error) {
	raw, err := h.DB.GetSetting(ctx, resolvedKey(project, fqService))
	if err != nil || raw == "" {
		return map[string]time.Time{}, err
	}
	out := map[string]time.Time{}
	if jerr := json.Unmarshal([]byte(raw), &out); jerr != nil {
		// A corrupt mark only un-hides groups; don't fail the read.
		h.Logger.Warn("errors: decode resolved marks", "project", project, "service", fqService, "err", jerr)
		return map[string]time.Time{}, nil
	}
	return out, nil
}

func (h *ErrorsHandler) saveResolved(ctx context.Context, project, fqService string, marks map[string]time.Time) error {
	cutoff := time.Now().Add(-resolvedRetention)
	for fp, at := range marks {
		if at.Before(cutoff) {
			delete(marks, fp)
		}
	}
	b, err := json.Marshal(marks)
	if err != nil {
		return err
	}
	return h.DB.SetSetting(ctx, resolvedKey(project, fqService), string(b), auditUser(ctx))
}

// applyResolved stamps resolvedAt on groups and, unless include is set,
// drops the ones with no occurrence since they were resolved.
func applyResolved(groups []db.ErrorGroup, marks map[string]time.Time, include bool) []db.ErrorGroup {
	if len(marks) == 0 {
		return groups
	}
	out := groups[:0]
	for _, g := range groups {
		at, ok := marks[g.Fingerprint]
		if ok {
			t := at
			g.ResolvedAt = &t
			if !include && !g.LastSeen.After(at) {
				continue
			}
		}
		out = append(out, g)
	}
	return out
}

// Resolve marks one error group resolved (POST) or reopens it (DELETE).
func (h *ErrorsHandler) Resolve(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	project := chi.URLParam(r, "project")
	service := chi.URLParam(r, "service")
	fingerprint := chi.URLParam(r, "fingerprint")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleEditor) {
		return
	}
	if !requireParent(ctx, w, h.Kube, h.Namespace, project, service) {
		return
	}
	if fingerprint == "" || len(fingerprint) > 64 || strings.ContainsAny(fingerprint, " \t\r\n") {
		writeErr(w, http.StatusBadRequest, "invalid fingerprint")
		return
	}
	if !strings.HasPrefix(service, project+"-") {
		service = project + "-" + service
	}
	marks, err := h.loadResolved(ctx, project, service)
	if err != nil {
		h.Logger.Error("errors: load resolved", "err", err, "project", project, "service", service)
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	now := time.Now().UTC()
	if r.Method == http.MethodDelete {
		delete(marks, fingerprint)
	} else {
		marks[fingerprint] = now
	}
	if err := h.saveResolved(ctx, project, service, marks); err != nil {
		h.Logger.Error("errors: save resolved", "err", err, "project", project, "service", service)
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	if r.Method == http.MethodDelete {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"fingerprint": fingerprint, "resolvedAt": now.Format(time.RFC3339)})
}

// discountResolved removes resolved, not-recurred groups from a service's
// summary so the badge clears once every group is resolved. It reads the
// service's groups only when it has resolve marks.
func (h *ErrorsHandler) discountResolved(ctx context.Context, project, env string, since time.Time, s *db.ServiceErrorSummary) error {
	marks, err := h.loadResolved(ctx, project, s.Service)
	if err != nil || len(marks) == 0 {
		return err
	}
	groups, err := h.DB.ListErrorGroups(ctx, project, s.Service, env, since, 200, 0)
	if err != nil {
		return fmt.Errorf("list groups: %w", err)
	}
	for _, g := range groups {
		if at, ok := marks[g.Fingerprint]; ok && !g.LastSeen.After(at) {
			s.Occurrences -= g.Count
			s.Groups--
		}
	}
	s.Occurrences = max(s.Occurrences, 0)
	s.Groups = max(s.Groups, 0)
	return nil
}
