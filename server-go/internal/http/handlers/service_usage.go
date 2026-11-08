package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/db"
	"kuso/server/internal/kube"
	"kuso/server/internal/podusage"
)

// usageHistoryMaxPoints bounds the response; a 24h range at 30s would
// otherwise be 2880 points for a chart a few hundred pixels wide.
const usageHistoryMaxPoints = 300

// ServiceUsageHandler serves the in-memory CPU/memory history the
// podusage sampler keeps for each service env.
type ServiceUsageHandler struct {
	Store     *podusage.Store
	DB        *db.DB
	Kube      *kube.Client
	Namespace string
}

func (h *ServiceUsageHandler) Mount(r chi.Router) {
	r.Get("/api/projects/{project}/services/{service}/usage/history", h.History)
}

// History is GET .../usage/history?env=<KusoEnvironment name>&range=1h.
// range accepts the UI's presets and is capped at podusage.Retention.
func (h *ServiceUsageHandler) History(w http.ResponseWriter, r *http.Request) {
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
	env := r.URL.Query().Get("env")
	if env == "" {
		writeErr(w, http.StatusBadRequest, "missing env")
		return
	}
	rng := time.Hour
	if v := r.URL.Query().Get("range"); v != "" {
		d, err := parseRangeDuration(v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("invalid range %q: use a duration like 1h, 6h or 1d", v))
			return
		}
		rng = min(d, podusage.Retention)
	}
	key := podusage.Key{Project: project, Service: strings.TrimPrefix(service, project+"-"), Env: env}
	writeJSON(w, http.StatusOK, h.Store.History(key, time.Now(), rng, usageHistoryMaxPoints))
}
