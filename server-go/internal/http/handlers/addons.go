package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	apiv1 "github.com/sislelabs/kuso/api/apiv1"

	"kuso/server/internal/addons"
	"kuso/server/internal/audit"
	"kuso/server/internal/auth"
	"kuso/server/internal/db"
	"kuso/server/internal/kube"
)

// apiv1CreateAddonToDomain converts the wire shape to the internal
// request. apiv1 owns the JSON contract; addons.CreateAddonRequest
// stays as the domain shape so internal callers (preview-DB clone,
// instance-addon resolver) can keep using their private fields
// without round-tripping through the wire type.
func apiv1CreateAddonToDomain(in apiv1.CreateAddonRequest) addons.CreateAddonRequest {
	out := addons.CreateAddonRequest{
		Name:             in.Name,
		Kind:             in.Kind,
		Version:          in.Version,
		Size:             in.Size,
		HA:               in.HA,
		StorageSize:      in.StorageSize,
		Database:         in.Database,
		UseInstanceAddon: in.UseInstanceAddon,
	}
	if in.External != nil {
		out.External = &kube.KusoAddonExternal{
			SecretName: in.External.SecretName,
			SecretKeys: in.External.SecretKeys,
		}
	}
	if in.Pooler != nil {
		out.Pooler = &kube.KusoAddonPooler{Enabled: in.Pooler.Enabled}
	}
	return out
}

// apiv1UpdateAddonToDomain converts the wire PATCH shape. Pointer
// semantics preserved end-to-end.
func apiv1UpdateAddonToDomain(in apiv1.UpdateAddonRequest) addons.UpdateAddonRequest {
	out := addons.UpdateAddonRequest{
		Version:     in.Version,
		Size:        in.Size,
		HA:          in.HA,
		StorageSize: in.StorageSize,
		Database:    in.Database,
	}
	if in.Backup != nil {
		out.Backup = &addons.UpdateBackupPatch{
			Schedule:      in.Backup.Schedule,
			RetentionDays: in.Backup.RetentionDays,
		}
	}
	if in.Pooler != nil {
		out.Pooler = &addons.AddonPoolerPatch{Enabled: &in.Pooler.Enabled}
	}
	return out
}

// AddonsHandler exposes the /api/projects/:p/addons routes.
type AddonsHandler struct {
	Svc    *addons.Service
	DB     *db.DB
	Audit  *audit.Service
	Logger *slog.Logger
}

// Mount registers the routes onto the given chi router.
func (h *AddonsHandler) Mount(r chi.Router) {
	r.Get("/api/projects/{project}/addons", h.List)
	r.Post("/api/projects/{project}/addons", h.Add)
	r.Delete("/api/projects/{project}/addons/{addon}", h.Delete)
	// Edit version / size / HA / storageSize / database.
	r.Patch("/api/projects/{project}/addons/{addon}", h.Update)
	// Pin the addon's StatefulSet to a subset of nodes. PUT replaces
	// the placement struct verbatim; pass empty {} to clear.
	r.Put("/api/projects/{project}/addons/{addon}/placement", h.Placement)
	r.Get("/api/projects/{project}/addons/{addon}/secret-keys", h.SecretKeys)
	// Plaintext connection values. Gated behind secrets:read at the
	// router level so the autocomplete (keys-only) endpoint above
	// stays open to anyone with addons:read.
	r.Get("/api/projects/{project}/addons/{addon}/secret", h.Secret)
	// Re-mirror the source Secret into the addon's <name>-conn for
	// external addons. Useful after the upstream credentials rotated.
	r.Post("/api/projects/{project}/addons/{addon}/resync-external", h.ResyncExternal)
	// Re-provision the per-project DB on a shared instance addon
	// (Model 2). Rotates the password and refreshes <name>-conn.
	r.Post("/api/projects/{project}/addons/{addon}/resync-instance", h.ResyncInstance)
	// Recover from the helm-chart password drift bug: ALTER USER
	// inside the running postgres pod to match the conn secret.
	r.Post("/api/projects/{project}/addons/{addon}/repair-password", h.RepairPassword)
	// Opt-in public TCP endpoint. POST allocates a port from the
	// configured pool and stamps spec.publicTCP; DELETE frees it.
	// Admin-only — a public database is an attack surface.
	r.Post("/api/projects/{project}/addons/{addon}/public-tcp", h.EnablePublicTCP)
	r.Delete("/api/projects/{project}/addons/{addon}/public-tcp", h.DisablePublicTCP)
}

// EnablePublicTCP opts the addon into a public TCP endpoint,
// allocating a port from the cluster's configured pool. Returns
// {port: <N>} on success. Admin-gated.
func (h *AddonsHandler) EnablePublicTCP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := addonsCtx(r)
	defer cancel()
	if !requireAdmin(w, r) {
		return
	}
	project := chi.URLParam(r, "project")
	name := chi.URLParam(r, "addon")
	port, err := h.Svc.EnablePublicTCP(ctx, project, name)
	if err != nil {
		h.fail(w, "enable public-tcp", err)
		return
	}
	if h.Audit != nil {
		h.Audit.Log(ctx, audit.Entry{
			User:     auditUser(ctx),
			Severity: "warn",
			Action:   "addon.public_tcp_enable",
			Pipeline: project,
			App:      name,
			Resource: "kusoaddon",
			Message:  fmt.Sprintf("addon %s/%s exposed on public TCP port %d", project, name, port),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"port": port})
}

// DisablePublicTCP frees the addon's allocated TCP port back to the
// pool and removes the Traefik IngressRouteTCP on the next reconcile.
// Admin-gated. Idempotent.
func (h *AddonsHandler) DisablePublicTCP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := addonsCtx(r)
	defer cancel()
	if !requireAdmin(w, r) {
		return
	}
	project := chi.URLParam(r, "project")
	name := chi.URLParam(r, "addon")
	if err := h.Svc.DisablePublicTCP(ctx, project, name); err != nil {
		h.fail(w, "disable public-tcp", err)
		return
	}
	if h.Audit != nil {
		h.Audit.Log(ctx, audit.Entry{
			User:     auditUser(ctx),
			Severity: "info",
			Action:   "addon.public_tcp_disable",
			Pipeline: project,
			App:      name,
			Resource: "kusoaddon",
			Message:  fmt.Sprintf("addon %s/%s public TCP endpoint removed", project, name),
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

// RepairPassword resyncs the running postgres user's password to
// match the conn secret. Use after the chart's password-reuse
// lookup raced and generated a fresh random while pgdata was
// locked to the old one.
func (h *AddonsHandler) RepairPassword(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := addonsCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleEditor) {
		return
	}
	if err := h.Svc.RepairPassword(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "addon")); err != nil {
		h.fail(w, "repair addon password", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ResyncInstance re-provisions the per-project DB on a shared
// instance addon and rotates the password.
func (h *AddonsHandler) ResyncInstance(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := addonsCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleEditor) {
		return
	}
	if err := h.Svc.ResyncInstanceAddon(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "addon")); err != nil {
		h.fail(w, "resync instance addon", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ResyncExternal triggers a re-mirror of the user-provided Secret
// for an external addon. 404 if not external.
func (h *AddonsHandler) ResyncExternal(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := addonsCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleEditor) {
		return
	}
	if err := h.Svc.ResyncExternal(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "addon")); err != nil {
		h.fail(w, "resync external addon", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Secret returns the addon's connection secret as a key→value map.
// Plaintext — gate at the router with secrets:read. Used by the addon
// overview's "Connection" panel so the user can copy DATABASE_URL etc.
// to connect from psql / their app / a tunnel.
func (h *AddonsHandler) Secret(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := addonsCtx(r)
	defer cancel()
	project := chi.URLParam(r, "project")
	// Plaintext connection values (DB passwords, DATABASE_URL, etc.) are
	// secret VALUES — admin-only in role-system v2, the same boundary as
	// reading env values / opening a shell. Editors can manage the addon
	// but must not siphon its credentials. SecretKeys (names only) stays
	// at viewer.
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleViewer) {
		return
	}
	if !callerCanReadSecrets(ctx, h.DB, project) {
		http.Error(w, "forbidden: reading addon connection values requires the admin role", http.StatusForbidden)
		return
	}
	values, err := h.Svc.SecretValues(ctx, project, chi.URLParam(r, "addon"))
	if err != nil {
		h.fail(w, "addon secret", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"values": values})
}

// SecretKeys is GET /api/projects/{project}/addons/{addon}/secret-keys.
// Lists keys in the addon's connection secret without ever exposing the
// values. Used by the frontend ${{ }} reference autocomplete.
func (h *AddonsHandler) SecretKeys(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := addonsCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleViewer) {
		return
	}
	keys, err := h.Svc.SecretKeys(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "addon"))
	if err != nil {
		h.fail(w, "addon secret keys", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
}

func addonsCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 10*time.Second)
}

func (h *AddonsHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := addonsCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleViewer) {
		return
	}
	out, err := h.Svc.List(ctx, chi.URLParam(r, "project"))
	if err != nil {
		h.fail(w, "list addons", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *AddonsHandler) Add(w http.ResponseWriter, r *http.Request) {
	var wire apiv1.CreateAddonRequest
	if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := addonsCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleEditor) {
		return
	}
	project := chi.URLParam(r, "project")
	req := apiv1CreateAddonToDomain(wire)
	out, err := h.Svc.Add(ctx, project, req)
	if err != nil {
		h.fail(w, "add addon", err)
		return
	}
	if h.Audit != nil {
		// Addon provisioning is privileged: it allocates persistent
		// storage and (for Postgres/Redis/etc) generates credentials
		// auto-injected into every service env in the project.
		h.Audit.Log(ctx, audit.Entry{
			User:     auditUser(ctx),
			Severity: "info",
			Action:   "addon.create",
			Pipeline: project,
			App:      req.Name,
			Resource: "kusoaddon",
			Message:  fmt.Sprintf("created addon %q (%s) in project %q", req.Name, req.Kind, project),
		})
	}
	writeJSON(w, http.StatusCreated, out)
}

// Update applies a partial update to the addon spec. Body shape is
// apiv1.UpdateAddonRequest — pointer fields, nil means "leave
// alone". Returns the updated CR so the UI can re-baseline.
func (h *AddonsHandler) Update(w http.ResponseWriter, r *http.Request) {
	var wire apiv1.UpdateAddonRequest
	if err := json.NewDecoder(r.Body).Decode(&wire); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := addonsCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleEditor) {
		return
	}
	out, err := h.Svc.Update(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "addon"), apiv1UpdateAddonToDomain(wire))
	if err != nil {
		h.fail(w, "update addon", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// Placement updates spec.placement on a KusoAddon CR. Body shape
// matches kube.KusoPlacement: {labels: {…}, nodes: […]}. An empty
// body / null clears placement (schedule anywhere).
func (h *AddonsHandler) Placement(w http.ResponseWriter, r *http.Request) {
	var body kube.KusoPlacement
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := addonsCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleEditor) {
		return
	}
	// Pass nil when both fields are empty so the server stores no
	// placement at all (and the helm chart skips the nodeSelector
	// block) instead of an empty struct that some clients might
	// surface as "is set, just empty."
	var p *kube.KusoPlacement
	if len(body.Labels) > 0 || len(body.Nodes) > 0 {
		p = &body
	}
	if err := h.Svc.SetPlacement(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "addon"), p); err != nil {
		h.fail(w, "addon placement", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *AddonsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := addonsCtx(r)
	defer cancel()
	project := chi.URLParam(r, "project")
	addon := chi.URLParam(r, "addon")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleEditor) {
		return
	}
	// Typed-confirmation guard: caller must echo the addon name in
	// ?confirm=<addon>. The PVC's reclaim policy is the cluster
	// default (Delete on a stock k3s install) — without this gate a
	// stray DELETE wipes the customer DB with no recovery. The UI
	// makes the user type the addon name into a confirm field;
	// CLI / API callers pass it explicitly.
	if r.URL.Query().Get("confirm") != addon {
		http.Error(w, "addon delete requires ?confirm=<addon-name> to acknowledge data loss", http.StatusBadRequest)
		return
	}
	if err := h.Svc.Delete(ctx, project, addon); err != nil {
		h.fail(w, "delete addon", err)
		return
	}
	if h.Audit != nil {
		uid := ""
		if c, ok := auth.ClaimsFromContext(ctx); ok && c != nil {
			uid = c.UserID
		}
		h.Audit.Log(ctx, audit.Entry{
			User:     uid,
			Severity: "warn",
			Action:   "addon.delete",
			Pipeline: project,
			App:      addon,
			Resource: "kusoaddon",
			Message:  "addon deleted (data PVC reclaim depends on StorageClass)",
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *AddonsHandler) fail(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, addons.ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, addons.ErrConflict):
		// Pass the wrapped error through so the UI can show
		// "addon kuso-hello-go/postgres already exists" instead of
		// a bare "409 Conflict" toast.
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, addons.ErrInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		h.Logger.Error("addons handler", "op", op, "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
	}
}
