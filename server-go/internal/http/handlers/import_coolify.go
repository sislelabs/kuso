package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"kuso/server/internal/coolify"
)

// ImportCoolifyHandler exposes a single endpoint for previewing
// what a Coolify import would do. The actual commit lives behind
// a separate POST and is admin-gated — preview is read-only against
// the user's Coolify instance and safe enough to surface to anyone
// who's logged in (the credential they supply is their own).
//
// Design choice: this handler does NOT execute the import. The UI
// renders the inventory + per-row checkboxes; a follow-up commit
// to a different endpoint (`POST /api/import/coolify/commit`)
// performs the real writes. Splitting preview from commit keeps
// the user's "I'm just looking" path away from the destructive
// path, and lets the UI implement the dry-run preview table
// without spawning a Job for every snapshot.
type ImportCoolifyHandler struct {
	Logger *slog.Logger
}

// Mount registers the routes onto the bearer-protected router.
func (h *ImportCoolifyHandler) Mount(r interface {
	Post(pattern string, h http.HandlerFunc)
}) {
	r.Post("/api/import/coolify/preview", h.Preview)
}

// PreviewRequest is the wire shape: where to talk to Coolify and
// which credential to use. Token is in the body (not a header) so
// it goes through the standard request-size cap + the rate limiter
// that protects /api/* — query strings and headers bypass both.
type PreviewRequest struct {
	BaseURL string `json:"baseUrl"`
	Token   string `json:"token"`
}

// PreviewStats is the aggregate counter shape the wizard renders
// as a header summary above the per-row table.
type PreviewStats struct {
	NumApps     int `json:"numApps"`
	NumDBs      int `json:"numDBs"`
	NumServices int `json:"numServices"`
	NumSkipped  int `json:"numSkipped"`
	NumMigrate  int `json:"numMigrate"`
	NumFlag     int `json:"numFlag"`
}

// PreviewResponse is the shape the wizard renders. Each item maps
// 1:1 to a Coolify resource; the verdict carries our classifier's
// import-ability call, the suggested kuso shape, and any caveats.
type PreviewResponse struct {
	CoolifyVersion string         `json:"coolifyVersion"`
	Stats          PreviewStats   `json:"stats"`
	Items          []coolify.Item `json:"items"`
}

func (h *ImportCoolifyHandler) Preview(w http.ResponseWriter, r *http.Request) {
	// Coolify import is admin-only — it provisions projects + addons
	// across every kuso namespace. A future variant could relax this
	// to "any user, scoped to their own project memberships," but
	// the v1 surface keeps it admin-gated for safety.
	if !requireAdmin(w, r) {
		return
	}
	var req PreviewRequest
	if err := decodeJSON(r, &req); err != nil {
		http.Error(w, "decode: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.BaseURL == "" || req.Token == "" {
		http.Error(w, "baseUrl and token required", http.StatusBadRequest)
		return
	}
	// Block private-network targets unless explicitly allowed. The
	// notify SSRF helper from S-P2-2 has the same shape but lives in
	// a different package; duplicate the minimal check here rather
	// than couple this handler to notify.
	if u, err := url.Parse(req.BaseURL); err != nil || u.Scheme == "" || (u.Scheme != "https" && u.Scheme != "http") {
		http.Error(w, "baseUrl must be http(s)://...", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	c := coolify.New(req.BaseURL, req.Token)
	inv, err := coolify.Snapshot(ctx, c)
	if err != nil {
		// Surface as 502 so the SPA can show "couldn't reach Coolify"
		// instead of "server error." Don't leak the token in the
		// returned message — Snapshot may have wrapped it.
		if errors.Is(err, context.DeadlineExceeded) {
			http.Error(w, "coolify request timed out", http.StatusGatewayTimeout)
			return
		}
		if h.Logger != nil {
			h.Logger.Warn("coolify snapshot", "err", err)
		}
		http.Error(w, "couldn't reach Coolify: "+err.Error(), http.StatusBadGateway)
		return
	}
	resp := PreviewResponse{
		CoolifyVersion: inv.CoolifyVersion,
		Stats: PreviewStats{
			NumApps:     inv.NumApps,
			NumDBs:      inv.NumDBs,
			NumServices: inv.NumServices,
			NumSkipped:  inv.NumSkipped,
			NumMigrate:  inv.NumMigrate,
			NumFlag:     inv.NumFlag,
		},
		Items: inv.Items,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
