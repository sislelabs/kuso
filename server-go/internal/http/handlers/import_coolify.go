package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/sislelabs/kuso/coolify"

	"kuso/server/internal/httpx"
	"kuso/server/internal/migration"
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
	// Migration owns the provisioning orchestration; the handler is
	// a thin adapter that validates input, runs the Coolify
	// snapshot, and hands the result to migration.ImportCoolify.
	// Old shape had 270 lines of project/service/addon walking
	// inside the handler file — split out per architecture review
	// B-01 so a future second importer (Heroku, Render) can plug
	// in without re-growing this file.
	Migration *migration.Service
}

// Mount registers the routes onto the bearer-protected router.
func (h *ImportCoolifyHandler) Mount(r interface {
	Post(pattern string, h http.HandlerFunc)
}) {
	r.Post("/api/import/coolify/preview", h.Preview)
	r.Post("/api/import/coolify/commit", h.Commit)
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
	if err := decodeJSON(w, r, &req); err != nil {
		http.Error(w, "decode: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.BaseURL == "" || req.Token == "" {
		http.Error(w, "baseUrl and token required", http.StatusBadRequest)
		return
	}
	if u, err := url.Parse(req.BaseURL); err != nil || u.Scheme == "" || (u.Scheme != "https" && u.Scheme != "http") {
		http.Error(w, "baseUrl must be http(s)://...", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	// SSRF guard: refuse to dial RFC1918 / loopback / link-local
	// (catches http://10.96.0.1 = kube apiserver,
	// http://169.254.169.254 = cloud metadata). Admin-only doesn't
	// excuse it — admins should still not be able to pivot kuso's
	// SA token toward the kube API via SSRF. Operators on
	// fully-internal Coolify installs can opt in via
	// KUSO_ALLOW_PRIVATE_OUTBOUND=true.
	c := coolify.NewWithTransport(req.BaseURL, req.Token, httpx.SSRFSafeTransport())
	inv, err := coolify.Snapshot(ctx, c)
	if err != nil {
		// Surface as 502 so the SPA can show "couldn't reach Coolify"
		// instead of "server error." Don't leak err.Error() to the
		// client: coolify.getRaw embeds up to 256 bytes of the
		// upstream response body in its error, which compounds the
		// SSRF concern — an internal target (kube apiserver,
		// metadata service) would surface its error body inside a
		// 502. Detailed error stays in slog; the wire response is
		// generic.
		if errors.Is(err, context.DeadlineExceeded) {
			http.Error(w, "coolify request timed out", http.StatusGatewayTimeout)
			return
		}
		if h.Logger != nil {
			h.Logger.Warn("coolify snapshot", "err", err)
		}
		http.Error(w, "couldn't reach Coolify (check server logs for detail)", http.StatusBadGateway)
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

// CommitRequest is the wire shape for POST /api/import/coolify/commit.
// The wizard re-runs Snapshot server-side using the same credentials
// so we don't have to trust client-supplied verdict rows — the server
// classifier is the only source of truth for what's importable. The
// caller passes the set of Coolify resource UUIDs they've ticked on
// the preview table; the commit handler creates projects + services
// + addons for that subset and skips everything else.
//
// Re-snapshotting on commit instead of round-tripping verdicts also
// closes a TOCTOU: an attacker who could tamper with the verdict
// list could otherwise smuggle skip-classified rows into the create
// path. By keeping classify→commit hermetic on the server, the
// client can't escalate the import beyond what preview agreed to.
type CommitRequest struct {
	BaseURL string   `json:"baseUrl"`
	Token   string   `json:"token"`
	UUIDs   []string `json:"uuids"`
}

// CommitResponse + CommitDetail are kept as type aliases for the
// wire shape — the migration package owns the implementation. Web
// + CLI consumers see the same JSON either way.
type CommitResponse = migration.Result
type CommitDetail = migration.Detail

func (h *ImportCoolifyHandler) Commit(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if h.Migration == nil {
		http.Error(w, "commit endpoint not configured (kube unavailable)", http.StatusServiceUnavailable)
		return
	}
	var req CommitRequest
	if err := decodeJSON(w, r, &req); err != nil {
		http.Error(w, "decode: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.BaseURL == "" || req.Token == "" {
		http.Error(w, "baseUrl and token required", http.StatusBadRequest)
		return
	}
	if u, err := url.Parse(req.BaseURL); err != nil || u.Scheme == "" || (u.Scheme != "https" && u.Scheme != "http") {
		http.Error(w, "baseUrl must be http(s)://...", http.StatusBadRequest)
		return
	}
	if len(req.UUIDs) == 0 {
		http.Error(w, "select at least one resource to import", http.StatusBadRequest)
		return
	}
	// Cap selection: a Coolify with thousands of resources shouldn't
	// be importable in one shot. The wizard chunks into smaller
	// commits if the user really wants everything.
	const maxSelection = 500
	if len(req.UUIDs) > maxSelection {
		http.Error(w, fmt.Sprintf("too many resources selected (max %d)", maxSelection), http.StatusBadRequest)
		return
	}

	// Long commit budget — a 50-app import does ~150 kube writes
	// against a busy operator. 5 min gives the long tail headroom
	// without holding the request open indefinitely if the upstream
	// stalls.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	c := coolify.NewWithTransport(req.BaseURL, req.Token, httpx.SSRFSafeTransport())
	inv, err := coolify.Snapshot(ctx, c)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			http.Error(w, "coolify request timed out", http.StatusGatewayTimeout)
			return
		}
		if h.Logger != nil {
			h.Logger.Warn("coolify commit snapshot", "err", err)
		}
		http.Error(w, "couldn't reach Coolify (check server logs for detail)", http.StatusBadGateway)
		return
	}

	picked := make(map[string]struct{}, len(req.UUIDs))
	for _, u := range req.UUIDs {
		picked[u] = struct{}{}
	}
	resp := h.Migration.ImportCoolify(ctx, c, inv, picked)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// Mapping helpers (slugify, runtime classification, port parsing,
// repo-URL normalisation, item UUID/kind) live in
// github.com/sislelabs/kuso/coolify as Coolify.{Helper}. The
// importer + CLI share that one canonical implementation; see
// coolify/mapping.go for the per-helper rationale.
