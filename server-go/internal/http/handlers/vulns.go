package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/addons"
	"kuso/server/internal/audit"
	"kuso/server/internal/auth"
	"kuso/server/internal/db"
	"kuso/server/internal/kube"
	"kuso/server/internal/projects"
	"kuso/server/internal/vulnscan"
)

// VulnServiceLookup resolves a project-owned service (projects.Service).
type VulnServiceLookup interface {
	GetService(ctx context.Context, project, service string) (*kube.KusoService, error)
}

// VulnAddonLookup resolves a project-owned addon (addons.Service). The
// ownership check is the point: resolving by CR name alone has been a
// recurring cross-project IDOR.
type VulnAddonLookup interface {
	GetOwned(ctx context.Context, project, name string) (*kube.KusoAddon, error)
}

// VulnLive finds the images workloads run now (vulnscan.Live).
type VulnLive interface {
	ServiceImage(ctx context.Context, project, service, env string) (*db.VulnImage, error)
	AddonImage(ctx context.Context, project string, a *kube.KusoAddon) (*db.VulnImage, error)
	ProjectImages(ctx context.Context, project string) ([]db.VulnImage, error)
}

// VulnsHandler serves vulnerability scanning (docs/VULN_SCANNING.md).
type VulnsHandler struct {
	DB       *db.DB
	Services VulnServiceLookup
	Addons   VulnAddonLookup
	Live     VulnLive
	// Sweep runs the nightly sweep now (scan-all); returns scans queued.
	Sweep func(ctx context.Context) (int, error)
	// Updater backs the addon "update image" action; nil disables it.
	Updater *vulnscan.AddonUpdater
	// OnSettingsChange drops the gate's cached settings.
	OnSettingsChange func()
	// Audit records policy and ignore writes. nil = not audited.
	Audit  *audit.Service
	Logger *slog.Logger
	Now    func() time.Time
}

func (h *VulnsHandler) Mount(r chi.Router) {
	r.Get("/api/projects/{project}/vulns", h.ProjectList)

	r.Get("/api/projects/{project}/services/{service}/vulns", h.ServiceShow)
	r.Post("/api/projects/{project}/services/{service}/vulns/scan", h.ServiceScan)
	r.Get("/api/projects/{project}/services/{service}/vulns/policy", h.GetPolicy)
	r.Put("/api/projects/{project}/services/{service}/vulns/policy", h.PutPolicy)
	r.Get("/api/projects/{project}/services/{service}/vulns/ignores", h.ServiceIgnores)
	r.Post("/api/projects/{project}/services/{service}/vulns/ignores", h.ServiceIgnoreAdd)
	r.Delete("/api/projects/{project}/services/{service}/vulns/ignores/{id}", h.ServiceIgnoreDelete)

	r.Get("/api/projects/{project}/addons/{addon}/vulns", h.AddonShow)
	r.Post("/api/projects/{project}/addons/{addon}/vulns/scan", h.AddonScan)
	r.Post("/api/projects/{project}/addons/{addon}/vulns/update-image", h.AddonUpdateImage)
	r.Get("/api/projects/{project}/addons/{addon}/vulns/ignores", h.AddonIgnores)
	r.Post("/api/projects/{project}/addons/{addon}/vulns/ignores", h.AddonIgnoreAdd)
	r.Delete("/api/projects/{project}/addons/{addon}/vulns/ignores/{id}", h.AddonIgnoreDelete)

	r.Get("/api/admin/vulns/platform", h.PlatformList)
	r.Get("/api/admin/vulns/platform/findings", h.PlatformFindings)
	r.Post("/api/admin/vulns/scan-all", h.ScanAll)
	r.Get("/api/admin/vulns/scans/{id}", h.GetScan)
	r.Get("/api/admin/settings/vulnscan", h.GetSettings)
	r.Put("/api/admin/settings/vulnscan", h.PutSettings)
}

var (
	errVulnNothingToScan = errors.New("nothing to scan")
	vulnIDRE             = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:+-]{1,127}$`)
)

const maxIgnoreDays = 365

func (h *VulnsHandler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *VulnsHandler) reports() *vulnscan.Reports {
	return &vulnscan.Reports{DB: h.DB, Now: h.Now}
}

// fail maps domain errors to statuses, passing the message through.
func (h *VulnsHandler) fail(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, projects.ErrNotFound):
		writeErr(w, http.StatusNotFound, notFoundMsg(err, projects.ErrNotFound, "service"))
	case errors.Is(err, addons.ErrNotFound):
		writeErr(w, http.StatusNotFound, notFoundMsg(err, addons.ErrNotFound, "addon"))
	case errors.Is(err, db.ErrNotFound):
		writeErr(w, http.StatusNotFound, notFoundMsg(err, db.ErrNotFound, kindFromOp(op)))
	case errors.Is(err, vulnscan.ErrInvalid):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, errVulnNothingToScan), errors.Is(err, vulnscan.ErrAlreadyCurrent), errors.Is(err, vulnscan.ErrUpdateInProgress):
		writeErr(w, http.StatusConflict, err.Error())
	default:
		if status, ok := kubeErrStatus(err); ok {
			writeErr(w, status, err.Error())
			return
		}
		h.Logger.Error("vulns handler", "op", op, "err", err)
		writeErr(w, http.StatusInternalServerError, "internal")
	}
}

// service gates on the project role, then resolves the service so an
// unknown one is a 404.
func (h *VulnsHandler) service(w http.ResponseWriter, r *http.Request, role db.ProjectRole) (project, service string, ok bool) {
	project, service = chi.URLParam(r, "project"), chi.URLParam(r, "service")
	if !requireProjectAccess(r.Context(), w, h.DB, project, role) {
		return "", "", false
	}
	if _, err := h.Services.GetService(r.Context(), project, service); err != nil {
		h.fail(w, "get service", err)
		return "", "", false
	}
	return project, service, true
}

func (h *VulnsHandler) addon(w http.ResponseWriter, r *http.Request, role db.ProjectRole) (string, *kube.KusoAddon, bool) {
	project := chi.URLParam(r, "project")
	if !requireProjectAccess(r.Context(), w, h.DB, project, role) {
		return "", nil, false
	}
	a, err := h.Addons.GetOwned(r.Context(), project, chi.URLParam(r, "addon"))
	if err != nil {
		h.fail(w, "get addon", err)
		return "", nil, false
	}
	return project, a, true
}

func addonShort(project string, a *kube.KusoAddon) string {
	return strings.TrimPrefix(a.Name, project+"-")
}

// findingsQuery parses ?severity= and ?includeIgnored= (default true).
func findingsQuery(r *http.Request) (map[string]bool, bool, error) {
	sev := map[string]bool{}
	if raw := r.URL.Query().Get("severity"); raw != "" {
		for _, s := range strings.Split(raw, ",") {
			s = strings.ToUpper(strings.TrimSpace(s))
			valid := false
			for _, k := range vulnscan.Severities {
				valid = valid || s == k
			}
			if !valid {
				return nil, false, fmt.Errorf("%w: unknown severity %q", vulnscan.ErrInvalid, s)
			}
			sev[s] = true
		}
	}
	include := true
	if raw := r.URL.Query().Get("includeIgnored"); raw != "" {
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, false, fmt.Errorf("%w: includeIgnored must be true or false", vulnscan.ErrInvalid)
		}
		include = b
	}
	return sev, include, nil
}

// detail renders {image, findings} for one live image (or null).
func (h *VulnsHandler) detail(r *http.Request, im *db.VulnImage) (map[string]any, error) {
	sev, include, err := findingsQuery(r)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"image": nil, "findings": []vulnscan.FindingJSON{}}
	if im == nil {
		return out, nil
	}
	d, err := h.reports().Detail(r.Context(), *im)
	if err != nil {
		return nil, err
	}
	out["image"] = d.Image
	out["findings"] = vulnscan.FindingsToJSON(d.Findings, sev, include)
	return out, nil
}

func (h *VulnsHandler) settings(ctx context.Context) (db.VulnScanSettings, error) {
	return h.DB.GetVulnScanSettings(ctx)
}

func (h *VulnsHandler) ProjectList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	project := chi.URLParam(r, "project")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleViewer) {
		return
	}
	st, err := h.settings(ctx)
	if err != nil {
		h.fail(w, "read settings", err)
		return
	}
	imgs, err := h.Live.ProjectImages(ctx, project)
	if err != nil {
		h.fail(w, "list images", err)
		return
	}
	reps, err := h.reports().ImageReports(ctx, imgs)
	if err != nil {
		h.fail(w, "list images", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mode": st.Mode, "images": reps})
}

func (h *VulnsHandler) envParam(r *http.Request) string {
	if env := r.URL.Query().Get("env"); env != "" {
		return env
	}
	return "production"
}

func (h *VulnsHandler) ServiceShow(w http.ResponseWriter, r *http.Request) {
	project, service, ok := h.service(w, r, db.ProjectRoleViewer)
	if !ok {
		return
	}
	ctx := r.Context()
	policy, effective, err := h.policy(ctx, project, service)
	if err != nil {
		h.fail(w, "read policy", err)
		return
	}
	im, err := h.Live.ServiceImage(ctx, project, service, h.envParam(r))
	if err != nil {
		h.fail(w, "find image", err)
		return
	}
	out, err := h.detail(r, im)
	if err != nil {
		h.fail(w, "read findings", err)
		return
	}
	out["policy"], out["effectiveMode"] = policy, effective
	writeJSON(w, http.StatusOK, out)
}

// scan queues a forced scan of a live image and records the sighting,
// so the owner is known when the result is alerted on.
func (h *VulnsHandler) scan(w http.ResponseWriter, r *http.Request, im *db.VulnImage) {
	ctx := r.Context()
	if im == nil {
		h.fail(w, "scan", fmt.Errorf("%w: no live image", errVulnNothingToScan))
		return
	}
	if err := h.DB.UpsertVulnImages(ctx, []db.VulnImage{*im}, h.now()); err != nil {
		h.fail(w, "scan", err)
		return
	}
	id, _, err := h.DB.EnqueueVulnScan(ctx, db.VulnScanRequest{
		Digest: im.Digest, ImageRef: im.ImageRef, Reason: db.VulnReasonManual, PullSecret: im.PullSecret,
	})
	if err != nil {
		h.fail(w, "scan", err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"scanId": id})
}

func (h *VulnsHandler) ServiceScan(w http.ResponseWriter, r *http.Request) {
	project, service, ok := h.service(w, r, db.ProjectRoleEditor)
	if !ok {
		return
	}
	var body struct {
		Env string `json:"env"`
	}
	if err := decodeJSON(w, r, &body); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	env := body.Env
	if env == "" {
		env = "production"
	}
	im, err := h.Live.ServiceImage(r.Context(), project, service, env)
	if err != nil {
		h.fail(w, "find image", err)
		return
	}
	h.scan(w, r, im)
}

func (h *VulnsHandler) policy(ctx context.Context, project, service string) (policy, effective string, err error) {
	st, err := h.settings(ctx)
	if err != nil {
		return "", "", err
	}
	policy, err = h.DB.GetVulnPolicy(ctx, project, service)
	if err != nil {
		return "", "", err
	}
	return policy, vulnscan.EffectiveMode(st.Mode, policy), nil
}

func (h *VulnsHandler) writePolicy(w http.ResponseWriter, r *http.Request, project, service string) {
	policy, effective, err := h.policy(r.Context(), project, service)
	if err != nil {
		h.fail(w, "read policy", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"mode": policy, "effectiveMode": effective})
}

func (h *VulnsHandler) GetPolicy(w http.ResponseWriter, r *http.Request) {
	project, service, ok := h.service(w, r, db.ProjectRoleViewer)
	if !ok {
		return
	}
	h.writePolicy(w, r, project, service)
}

func (h *VulnsHandler) PutPolicy(w http.ResponseWriter, r *http.Request) {
	project, service, ok := h.service(w, r, db.ProjectRoleEditor)
	if !ok {
		return
	}
	var body struct {
		Mode string `json:"mode"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if !vulnscan.ValidPolicy(body.Mode) {
		h.fail(w, "set policy", fmt.Errorf("%w: mode must be inherit, enforce, report or off", vulnscan.ErrInvalid))
		return
	}
	st, err := h.settings(r.Context())
	if err != nil {
		h.fail(w, "set policy", err)
		return
	}
	// An editor may tighten a service's gate but not loosen it below
	// what the instance admin set; that takes a project admin.
	if vulnscan.WeakerThanInstance(st.Mode, body.Mode) &&
		!requireProjectAccess(r.Context(), w, h.DB, project, db.ProjectRoleAdmin) {
		return
	}
	prev, _ := h.DB.GetVulnPolicy(r.Context(), project, service)
	if err := h.DB.SetVulnPolicy(r.Context(), project, service, body.Mode); err != nil {
		h.fail(w, "set policy", err)
		return
	}
	h.audit(r.Context(), "vulns.policy.set", project, service,
		fmt.Sprintf("vuln policy %s → %s (instance mode %s)", orInherit(prev), body.Mode, st.Mode))
	h.writePolicy(w, r, project, service)
}

func orInherit(p string) string {
	if p == "" {
		return vulnscan.ModeInherit
	}
	return p
}

func (h *VulnsHandler) audit(ctx context.Context, action, project, app, msg string) {
	if h.Audit == nil {
		return
	}
	h.Audit.Log(ctx, audit.Entry{
		User:     auditUser(ctx),
		Action:   action,
		Pipeline: project,
		App:      app,
		Resource: "vulns",
		Message:  msg,
	})
}

func (h *VulnsHandler) listIgnores(w http.ResponseWriter, r *http.Request, scope, project, name string) {
	igs, err := h.DB.ListVulnIgnores(r.Context(), scope, project, name)
	if err != nil {
		h.fail(w, "list ignores", err)
		return
	}
	now := h.now()
	out := make([]vulnscan.IgnoreJSON, 0, len(igs))
	for _, ig := range igs {
		out = append(out, vulnscan.IgnoreToJSON(ig, now))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *VulnsHandler) addIgnore(w http.ResponseWriter, r *http.Request, scope, project, name string) {
	var body struct {
		VulnID        string `json:"vulnId"`
		Reason        string `json:"reason"`
		ExpiresInDays *int   `json:"expiresInDays"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	body.VulnID, body.Reason = strings.TrimSpace(body.VulnID), strings.TrimSpace(body.Reason)
	switch {
	case !vulnIDRE.MatchString(body.VulnID):
		h.fail(w, "add ignore", fmt.Errorf("%w: vulnId %q is not a vulnerability ID", vulnscan.ErrInvalid, body.VulnID))
		return
	case body.Reason == "" || len(body.Reason) > 1000:
		h.fail(w, "add ignore", fmt.Errorf("%w: reason is required (max 1000 characters)", vulnscan.ErrInvalid))
		return
	case body.ExpiresInDays != nil && (*body.ExpiresInDays < 1 || *body.ExpiresInDays > maxIgnoreDays):
		h.fail(w, "add ignore", fmt.Errorf("%w: expiresInDays must be 1-%d", vulnscan.ErrInvalid, maxIgnoreDays))
		return
	}
	ig := db.VulnIgnore{Scope: scope, Project: project, Service: name, VulnID: body.VulnID, Reason: body.Reason}
	if body.ExpiresInDays != nil {
		ig.ExpiresAt = h.now().Add(time.Duration(*body.ExpiresInDays) * 24 * time.Hour)
	}
	if claims, ok := auth.ClaimsFromContext(r.Context()); ok {
		ig.CreatedBy = claims.Username
		if ig.CreatedBy == "" {
			ig.CreatedBy = claims.UserID
		}
	}
	created, err := h.DB.CreateVulnIgnore(r.Context(), ig)
	if err != nil {
		h.fail(w, "add ignore", err)
		return
	}
	h.audit(r.Context(), "vulns.ignore.add", project, name,
		fmt.Sprintf("ignore %s on %s %s: %s", body.VulnID, scope, name, body.Reason))
	writeJSON(w, http.StatusCreated, vulnscan.IgnoreToJSON(created, h.now()))
}

func (h *VulnsHandler) deleteIgnore(w http.ResponseWriter, r *http.Request, scope, project, name string) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid ignore id")
		return
	}
	if err := h.DB.DeleteVulnIgnore(r.Context(), id, scope, project, name); err != nil {
		h.fail(w, "delete ignore", err)
		return
	}
	h.audit(r.Context(), "vulns.ignore.delete", project, name, fmt.Sprintf("removed ignore #%d on %s %s", id, scope, name))
	w.WriteHeader(http.StatusNoContent)
}

func (h *VulnsHandler) ServiceIgnores(w http.ResponseWriter, r *http.Request) {
	if project, service, ok := h.service(w, r, db.ProjectRoleViewer); ok {
		h.listIgnores(w, r, db.VulnOwnerService, project, service)
	}
}

func (h *VulnsHandler) ServiceIgnoreAdd(w http.ResponseWriter, r *http.Request) {
	project, service, ok := h.service(w, r, db.ProjectRoleEditor)
	if !ok {
		return
	}
	// Under enforce an ignore lifts a deploy block, which is the same
	// loosening PutPolicy reserves for a project admin.
	_, effective, err := h.policy(r.Context(), project, service)
	if err != nil {
		h.fail(w, "add ignore", err)
		return
	}
	if effective == vulnscan.ModeEnforce && !callerHasProjectPerm(r.Context(), h.DB, project, auth.PermSecretsRead) {
		writeErr(w, http.StatusForbidden, "forbidden: while the vulnerability gate enforces, ignoring a finding requires the project admin role")
		return
	}
	h.addIgnore(w, r, db.VulnOwnerService, project, service)
}

func (h *VulnsHandler) ServiceIgnoreDelete(w http.ResponseWriter, r *http.Request) {
	if project, service, ok := h.service(w, r, db.ProjectRoleEditor); ok {
		h.deleteIgnore(w, r, db.VulnOwnerService, project, service)
	}
}

func (h *VulnsHandler) AddonShow(w http.ResponseWriter, r *http.Request) {
	project, a, ok := h.addon(w, r, db.ProjectRoleViewer)
	if !ok {
		return
	}
	im, err := h.Live.AddonImage(r.Context(), project, a)
	if err != nil {
		h.fail(w, "find image", err)
		return
	}
	out, err := h.detail(r, im)
	if err != nil {
		h.fail(w, "read findings", err)
		return
	}
	st, err := vulnscan.AddonUpdateStateFor(r.Context(), h.DB, *a)
	if err != nil {
		h.fail(w, "read image update state", err)
		return
	}
	settings, err := h.settings(r.Context())
	if err != nil {
		h.fail(w, "read settings", err)
		return
	}
	out["imageUpdate"] = map[string]any{
		"auto":          settings.AutoUpdateAddons && settings.ScanAddons && settings.Mode != vulnscan.ModeOff,
		"windowHourUTC": settings.NightlyHourUTC,
		"available":     h.Updater != nil && a.Spec.External == nil && a.Spec.UseInstanceAddon == "",
		"state":         st,
	}
	writeJSON(w, http.StatusOK, out)
}

// AddonUpdateImage pins the addon to its tag's newest image now. The
// addon restarts.
func (h *VulnsHandler) AddonUpdateImage(w http.ResponseWriter, r *http.Request) {
	_, a, ok := h.addon(w, r, db.ProjectRoleEditor)
	if !ok {
		return
	}
	if h.Updater == nil {
		writeErr(w, http.StatusServiceUnavailable, "image updates are not available on this server")
		return
	}
	st, err := h.Updater.UpdateNow(r.Context(), *a)
	if err != nil {
		h.fail(w, "update image", err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (h *VulnsHandler) AddonScan(w http.ResponseWriter, r *http.Request) {
	project, a, ok := h.addon(w, r, db.ProjectRoleEditor)
	if !ok {
		return
	}
	im, err := h.Live.AddonImage(r.Context(), project, a)
	if err != nil {
		h.fail(w, "find image", err)
		return
	}
	h.scan(w, r, im)
}

func (h *VulnsHandler) AddonIgnores(w http.ResponseWriter, r *http.Request) {
	if project, a, ok := h.addon(w, r, db.ProjectRoleViewer); ok {
		h.listIgnores(w, r, db.VulnOwnerAddon, project, addonShort(project, a))
	}
}

func (h *VulnsHandler) AddonIgnoreAdd(w http.ResponseWriter, r *http.Request) {
	if project, a, ok := h.addon(w, r, db.ProjectRoleEditor); ok {
		h.addIgnore(w, r, db.VulnOwnerAddon, project, addonShort(project, a))
	}
}

func (h *VulnsHandler) AddonIgnoreDelete(w http.ResponseWriter, r *http.Request) {
	if project, a, ok := h.addon(w, r, db.ProjectRoleEditor); ok {
		h.deleteIgnore(w, r, db.VulnOwnerAddon, project, addonShort(project, a))
	}
}

func (h *VulnsHandler) platformImages(ctx context.Context, digest string) ([]db.VulnImage, error) {
	rows, err := h.DB.ListVulnImages(ctx, db.VulnImageFilter{
		OwnerKind: db.VulnOwnerPlatform, Digest: digest, SeenSince: h.now().Add(-vulnscan.GoneAfter),
	})
	if err != nil {
		return nil, err
	}
	return vulnscan.LatestPerOwner(rows), nil
}

func (h *VulnsHandler) PlatformList(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	imgs, err := h.platformImages(r.Context(), "")
	if err != nil {
		h.fail(w, "list platform images", err)
		return
	}
	reps, err := h.reports().ImageReports(r.Context(), imgs)
	if err != nil {
		h.fail(w, "list platform images", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"images": reps})
}

func (h *VulnsHandler) PlatformFindings(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	digest := r.URL.Query().Get("digest")
	if digest == "" {
		writeErr(w, http.StatusBadRequest, "digest is required")
		return
	}
	imgs, err := h.platformImages(r.Context(), digest)
	if err != nil {
		h.fail(w, "platform findings", err)
		return
	}
	if len(imgs) == 0 {
		writeErr(w, http.StatusNotFound, "platform image not found")
		return
	}
	out, err := h.detail(r, &imgs[0])
	if err != nil {
		h.fail(w, "platform findings", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *VulnsHandler) ScanAll(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	n, err := h.Sweep(r.Context())
	if err != nil {
		h.fail(w, "scan all", err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": n})
}

func (h *VulnsHandler) GetScan(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid scan id")
		return
	}
	sc, err := h.DB.GetVulnScan(r.Context(), id)
	if err != nil {
		h.fail(w, "get scan", err)
		return
	}
	writeJSON(w, http.StatusOK, vulnscan.Summarize(sc, nil))
}

func (h *VulnsHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	st, err := h.settings(r.Context())
	if err != nil {
		h.fail(w, "read settings", err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (h *VulnsHandler) PutSettings(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	// Decode over the current settings so a client that doesn't know a
	// newer field (autoUpdateAddons) leaves it as it was.
	in, err := h.settings(r.Context())
	if err != nil {
		h.fail(w, "read settings", err)
		return
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := vulnscan.ValidateSettings(in); err != nil {
		h.fail(w, "write settings", err)
		return
	}
	by := ""
	if claims, ok := auth.ClaimsFromContext(r.Context()); ok {
		by = claims.Username
	}
	if err := h.DB.SetVulnScanSettings(r.Context(), in, by); err != nil {
		h.fail(w, "write settings", err)
		return
	}
	if h.OnSettingsChange != nil {
		h.OnSettingsChange()
	}
	writeJSON(w, http.StatusOK, in)
}
