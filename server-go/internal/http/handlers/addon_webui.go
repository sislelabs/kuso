package handlers

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/addons"
	"kuso/server/internal/auth"
	"kuso/server/internal/db"
	"kuso/server/internal/kube"
)

// AddonWebUIHandler reverse-proxies the addon's built-in HTTP console
// (mailpit's mail viewer, NATS monitor, ...) through the kuso server.
// No new ingress; access is gated by the caller's kuso session +
// addons:read permission. Kinds without a known UI port return 404.
//
// Why proxy instead of issuing an ingress per UI:
//   - Single sign-on: the user is already authenticated to kuso, no
//     extra password to manage or rotate.
//   - No DNS records, no LE cert per UI, no rate-limit cost.
//   - Kuso server already runs behind TLS, so the UI inherits that
//     posture without a separate ingress class config.
//
// Tradeoff: the kuso server holds the streaming connections in
// memory. Fine for administrative UIs used briefly; we'd revisit if
// someone tried to ship a high-traffic console this way.
type AddonWebUIHandler struct {
	Svc    *addons.Service
	DB     *db.DB
	Logger *slog.Logger
}

// addonWebUIPort returns the in-cluster HTTP port a given addon kind
// exposes its built-in web console on, or 0 if the kind has no
// known UI. Kept as a single source of truth so the handler + the
// dashboard's "is UI available?" check stay aligned.
//
// Why a static table: addons of the same kind always pin the same
// port via their helm chart (Service.spec.ports), so there's no
// per-instance variance to discover. A future kind with a configurable
// port would need a richer mechanism (read the Service ports), but
// every kind we ship today is fixed.
func addonWebUIPort(kind string) int32 {
	switch kind {
	case "mailpit":
		return 8025
	case "nats":
		return 8222
	}
	return 0
}

// Mount registers the proxy under /api/projects/{p}/addons/{a}/webui/*.
// Every HTTP method is forwarded; WebSocket upgrades pass through
// because httputil.ReverseProxy hands the hijacked connection back to
// the upstream Service unmodified.
func (h *AddonWebUIHandler) Mount(r chi.Router) {
	r.Handle("/api/projects/{project}/addons/{addon}/webui", http.HandlerFunc(h.Proxy))
	r.Handle("/api/projects/{project}/addons/{addon}/webui/*", http.HandlerFunc(h.Proxy))
}

// Proxy is the actual reverse-proxy entrypoint. Resolves the addon's
// in-cluster Service URL, strips the routing prefix, and lets
// httputil.ReverseProxy do the streaming. Errors during resolve
// surface as 4xx/5xx; transport errors during proxying are written
// by the proxy's own ErrorHandler.
func (h *AddonWebUIHandler) Proxy(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()

	project := chi.URLParam(r, "project")
	addon := chi.URLParam(r, "addon")

	// Permission: addons:read is enough — viewing the mail catcher
	// or NATS monitor is a read operation. Writes happen against the
	// addon itself (a mail being received, a stream being published),
	// not against kuso state.
	claims, _ := auth.ClaimsFromContext(ctx)
	if claims == nil || !auth.Has(claims.Permissions, auth.PermAddonsRead) {
		http.Error(w, "forbidden: requires addons:read", http.StatusForbidden)
		return
	}
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleDeployer) {
		return
	}

	// Resolve the addon CR — we need the kind to map to a UI port
	// and to confirm webUI is enabled on this instance.
	ns := h.Svc.NamespaceFor(ctx, project)
	fqn := h.Svc.AddonFQN(project, addon)
	cr, err := h.Svc.Kube.GetKusoAddon(ctx, ns, fqn)
	if err != nil {
		http.Error(w, "addon not found: "+err.Error(), http.StatusNotFound)
		return
	}
	if cr.Spec.WebUI == nil || !cr.Spec.WebUI.Enabled {
		http.Error(w, "webUI is not enabled on this addon", http.StatusNotFound)
		return
	}
	port := addonWebUIPort(cr.Spec.Kind)
	if port == 0 {
		http.Error(w, fmt.Sprintf("kind %q has no known web UI port", cr.Spec.Kind), http.StatusNotFound)
		return
	}

	// In-cluster target. The addon's Service is named the same as
	// the CR fqn and lives in the project's exec namespace. We talk
	// HTTP to the cluster-DNS name directly — no ingress, no TLS,
	// because we're inside the same cluster as the addon Service.
	target := &url.URL{
		Scheme: "http",
		Host:   fmt.Sprintf("%s.%s.svc.cluster.local:%d", fqn, ns, port),
	}

	// Strip the prefix so the upstream sees the URL it expects
	// ("/" for mailpit's SPA, not "/api/projects/X/addons/Y/webui/").
	// httputil.ReverseProxy doesn't strip path prefixes; we have to
	// rewrite r.URL.Path before handing it off.
	prefix := fmt.Sprintf("/api/projects/%s/addons/%s/webui", project, addon)
	r.URL.Path = strings.TrimPrefix(r.URL.Path, prefix)
	if r.URL.Path == "" {
		// SPAs commonly redirect "" → "/" on first load; without
		// this the upstream sees an empty path and 404s.
		r.URL.Path = "/"
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(rw http.ResponseWriter, _ *http.Request, perr error) {
		h.Logger.Warn("addon webui proxy error",
			"project", project, "addon", addon, "target", target.Host, "err", perr)
		http.Error(rw, "upstream addon unavailable", http.StatusBadGateway)
	}
	// Default Director rewrites Host header to the target — necessary
	// for upstreams that use Host for SPA route detection (mailpit
	// embeds an iframe-busting check). We let httputil set it.
	proxy.ServeHTTP(w, r)
}

// addonWebUIPortFor is exported as a helper for the addons handler's
// list response, so the dashboard knows whether to render the
// "Open Web UI" chip without having to teach the frontend the kind→port
// table.
func addonWebUIPortFor(cr *kube.KusoAddon) int32 {
	if cr == nil || cr.Spec.WebUI == nil || !cr.Spec.WebUI.Enabled {
		return 0
	}
	return addonWebUIPort(cr.Spec.Kind)
}
