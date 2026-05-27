// Pull-mode node-join handlers. Companion to the SSH-driven flow in
// kubernetes_node_lifecycle.go. The new VM curls a one-liner from
// kuso, retrieves K3S_URL + K3S_TOKEN by redeeming a single-use
// bootstrap token, and runs the standard k3s install. See
// docs/NODE_BOOTSTRAP.md for the operator UX.
//
// Route map (mounted in router.go):
//
//   POST   /api/kubernetes/nodes/bootstrap-tokens         (admin)  → mint
//   GET    /api/kubernetes/nodes/bootstrap-tokens         (admin)  → list pending
//   DELETE /api/kubernetes/nodes/bootstrap-tokens/{jti}   (admin)  → revoke
//   GET    /bootstrap?token=<jti>                         (public) → render shell script
//   POST   /bootstrap/register-node                       (public) → consume + return join params
//
// Only the two /bootstrap routes are unauthenticated. The token IS
// the auth there: 128-bit random, single-use, 15-min TTL.

package handlers

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"kuso/server/internal/audit"
	"kuso/server/internal/auth"
	"kuso/server/internal/db"
	"kuso/server/internal/nodejoin"
)

// splitHostPort is net.SplitHostPort by another name — keeps the
// import surface narrow.
var splitHostPort = net.SplitHostPort

// NodeBootstrapHandler holds the deps for the bootstrap-token surface.
// Kept as its own struct (rather than methods on KubernetesHandler) so
// the public /bootstrap routes can be mounted on the unauthenticated
// router without dragging the kube client through the public side.
type NodeBootstrapHandler struct {
	DB     *db.DB
	Audit  *audit.Service
	Logger *slog.Logger
}

// MountAdmin registers the admin-only token-management endpoints. Wire
// inside the bearer-protected group.
func (h *NodeBootstrapHandler) MountAdmin(rt interface {
	Get(string, http.HandlerFunc)
	Post(string, http.HandlerFunc)
	Delete(string, http.HandlerFunc)
}) {
	rt.Post("/api/kubernetes/nodes/bootstrap-tokens", h.MintToken)
	rt.Get("/api/kubernetes/nodes/bootstrap-tokens", h.ListPending)
	rt.Delete("/api/kubernetes/nodes/bootstrap-tokens/{jti}", h.RevokeToken)
}

// MountPublic registers the unauthenticated VM-side endpoints. The
// token in the URL/body is the credential — a leaked token grants
// "join one node" power for 15 minutes.
//
// Both routes are wrapped in the same per-IP rate limiter the invite
// redemption uses. 128-bit jti entropy already makes brute force
// infeasible offline, but the limiter caps the per-IP attempt rate
// so a leaked partial token + a sequential probe can't enumerate
// state through response-code differences. KUSO_TRUSTED_PROXIES
// gating is honoured by withRateLimit's clientIP() lookup so the
// proxy itself doesn't get throttled to one bucket.
func (h *NodeBootstrapHandler) MountPublic(rt interface {
	Get(string, http.HandlerFunc)
	Post(string, http.HandlerFunc)
}) {
	rt.Get("/bootstrap", RateLimitedInvite(h.ServeScript))
	rt.Post("/bootstrap/register-node", RateLimitedInvite(h.RegisterNode))
}

// MintToken creates a fresh single-use bootstrap token. Body:
//
//	{ "labels": {"region":"eu","tier":"premium"}, "nodeName": "...", "ttlSeconds": 900 }
//
// Response:
//
//	{ "jti", "expiresAt", "oneLiner", "labels", "nodeName" }
//
// `oneLiner` is the curl command the operator pastes on the new VM.
func (h *NodeBootstrapHandler) MintToken(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var body struct {
		Labels     map[string]string `json:"labels"`
		NodeName   string            `json:"nodeName"`
		TTLSeconds int               `json:"ttlSeconds"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	for k := range body.Labels {
		if k == "" {
			http.Error(w, "label key cannot be empty", http.StatusBadRequest)
			return
		}
	}
	ttl := nodejoin.DefaultTokenTTL
	if body.TTLSeconds > 0 {
		ttl = time.Duration(body.TTLSeconds) * time.Second
		if ttl < 60*time.Second {
			ttl = 60 * time.Second
		}
		if ttl > time.Hour {
			ttl = time.Hour
		}
	}
	jti, err := nodejoin.GenerateJTI()
	if err != nil {
		h.Logger.Error("nodejoin: generate jti", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	createdBy := ""
	if claims, ok := auth.ClaimsFromContext(r.Context()); ok {
		createdBy = claims.UserID
	}
	expiresAt := time.Now().UTC().Add(ttl)
	if err := h.DB.MintNodeBootstrapToken(r.Context(), db.NodeBootstrapToken{
		Cleartext: jti,
		ExpiresAt: expiresAt,
		Labels:    body.Labels,
		NodeName:  body.NodeName,
		CreatedBy: createdBy,
	}); err != nil {
		h.Logger.Error("mint bootstrap token", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	publicURL := publicBaseURL(r)
	// Audit-log the mint (warn severity — adding a node is a
	// privileged action). User identity comes from the JWT claims;
	// the cleartext jti is intentionally NOT logged (it would defeat
	// the storage hashing). The hash prefix is enough to correlate
	// against the joined-node row when we later see node.bootstrap.joined.
	if h.Audit != nil {
		h.Audit.Log(r.Context(), audit.Entry{
			User:     createdBy,
			Severity: "warn",
			Action:   "node.bootstrap.mint",
			Resource: "node",
			Message:  fmt.Sprintf("minted bootstrap token jti=%s… ttl=%s nodeName=%q labels=%v", db.HashJTI(jti)[:8], ttl, body.NodeName, body.Labels),
		})
	}
	// The cleartext jti is returned ONCE here. The list endpoint
	// surfaces only the JTIHash prefix from now on — operators who
	// lose this response need to revoke + re-mint.
	writeJSON(w, http.StatusCreated, nodejoin.MintedToken{
		JTI:       jti,
		JTIPrefix: db.HashJTI(jti)[:8],
		ExpiresAt: expiresAt,
		OneLiner:  nodejoin.BuildOneLiner(publicURL, jti),
		Labels:    body.Labels,
		NodeName:  body.NodeName,
	})
}

// ListPending returns unconsumed, unrevoked, unexpired tokens. The UI
// uses this to show "waiting for node X to call home".
//
// Token cleartext is NEVER returned here — only a short prefix of the
// hash so the operator can correlate the row against the cleartext
// they captured at mint time. The "oneLiner" field is gone for the
// same reason: serving the full curl one-liner here would defeat the
// hash-at-rest design (a stolen admin session could re-fetch every
// live join URL).
func (h *NodeBootstrapHandler) ListPending(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	rows, err := h.DB.ListPendingNodeBootstrapTokens(r.Context())
	if err != nil {
		h.Logger.Error("list pending bootstrap tokens", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, t := range rows {
		out = append(out, map[string]any{
			"jtiPrefix": t.JTIPrefix(),
			"jtiHash":   t.JTIHash, // the revoke handle
			"createdAt": t.CreatedAt,
			"expiresAt": t.ExpiresAt,
			"labels":    t.Labels,
			"nodeName":  t.NodeName,
			"createdBy": t.CreatedBy,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": out})
}

// RevokeToken cancels a pending token. The URL path takes the JTIHash
// (returned by ListPending). Idempotent: revoking an already-revoked
// token returns 204; revoking an already-consumed token also returns
// 204 (the join already happened — there's nothing left to cancel).
// 404 only when the hash is unknown.
func (h *NodeBootstrapHandler) RevokeToken(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	jtiHash := chiURLParam(r, "jti")
	if jtiHash == "" {
		http.Error(w, "missing jti", http.StatusBadRequest)
		return
	}
	if err := h.DB.RevokeNodeBootstrapToken(r.Context(), jtiHash); err != nil {
		if errors.Is(err, db.ErrTokenNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if errors.Is(err, db.ErrTokenAmbiguous) {
			http.Error(w, "prefix matches multiple tokens — supply more characters", http.StatusConflict)
			return
		}
		h.Logger.Error("revoke bootstrap token", "err", err, "jtiHash", jtiHash)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if h.Audit != nil {
		actor := ""
		if claims, ok := auth.ClaimsFromContext(r.Context()); ok {
			actor = claims.UserID
		}
		h.Audit.Log(r.Context(), audit.Entry{
			User:     actor,
			Severity: "warn",
			Action:   "node.bootstrap.revoke",
			Resource: "node",
			Message:  fmt.Sprintf("revoked bootstrap token jtiHash=%s…", safePrefix(jtiHash)),
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

// safePrefix returns the first 8 chars of a hash-like string, or the
// whole string if shorter. Used for audit messages so we don't print
// the full hash.
func safePrefix(s string) string {
	if len(s) < 8 {
		return s
	}
	return s[:8]
}

// ServeScript renders the bootstrap shell script for a given token.
// Public: token in the URL is the credential. We do NOT consume the
// token here — the agent on the VM POSTs to /register-node to atomically
// consume + retrieve K3S_URL+K3S_TOKEN. That separation lets curl
// retry safely if the pipe to sh dies mid-stream.
//
// Returns 410 Gone for consumed/expired/revoked tokens (script shell
// doesn't care about distinguishing them — operator just needs to know
// "your token is gone").
func (h *NodeBootstrapHandler) ServeScript(w http.ResponseWriter, r *http.Request) {
	jti := strings.TrimSpace(r.URL.Query().Get("token"))
	if jti == "" {
		http.Error(w, "missing token", http.StatusBadRequest)
		return
	}
	if _, err := h.DB.PeekNodeBootstrapToken(r.Context(), jti); err != nil {
		switch {
		case errors.Is(err, db.ErrTokenNotFound):
			http.Error(w, "token not found", http.StatusNotFound)
		case errors.Is(err, db.ErrTokenConsumed),
			errors.Is(err, db.ErrTokenExpired),
			errors.Is(err, db.ErrTokenRevoked):
			http.Error(w, err.Error(), http.StatusGone)
		default:
			h.Logger.Error("peek bootstrap token", "err", err)
			http.Error(w, "internal", http.StatusInternalServerError)
		}
		return
	}
	script, err := nodejoin.RenderScript(nodejoin.ScriptParams{
		PublicURL: publicBaseURL(r),
		JTI:       jti,
	})
	if err != nil {
		h.Logger.Error("render bootstrap script", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(script))
}

// RegisterNode is what the bootstrap script POSTs back. We:
//
//  1. Consume the token (single-use; replay → 410).
//  2. Read the k3s server token from the hostPath mount.
//  3. Merge operator labels with VM-reported facts (arch, instance-type).
//  4. Build the install command via the shared helper (so SSH and pull
//     paths use identical flag escaping).
//  5. Return everything the script needs in one JSON response.
//
// The script is the only legitimate caller — but the endpoint is
// public, so we treat all input as untrusted. Token consumption is the
// auth boundary; everything past it is logged + applied.
func (h *NodeBootstrapHandler) RegisterNode(w http.ResponseWriter, r *http.Request) {
	var req nodejoin.RegisterRequest
	if err := decodeJSON(w, r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	jti := strings.TrimSpace(req.Token)
	if jti == "" {
		http.Error(w, "missing token", http.StatusBadRequest)
		return
	}
	fromIP := clientIP(r)
	tok, err := h.DB.ConsumeNodeBootstrapToken(r.Context(), jti, fromIP)
	if err != nil {
		switch {
		case errors.Is(err, db.ErrTokenNotFound):
			http.Error(w, "token not found", http.StatusNotFound)
		case errors.Is(err, db.ErrTokenConsumed),
			errors.Is(err, db.ErrTokenExpired),
			errors.Is(err, db.ErrTokenRevoked):
			http.Error(w, err.Error(), http.StatusGone)
		default:
			h.Logger.Error("consume bootstrap token", "err", err)
			http.Error(w, "internal", http.StatusInternalServerError)
		}
		return
	}

	k3sToken, err := nodejoin.ReadServerToken()
	if err != nil {
		h.Logger.Error("nodejoin: read k3s token", "err", err)
		http.Error(w, "control plane unavailable", http.StatusServiceUnavailable)
		return
	}
	k3sURL := controlPlaneJoinURL()
	if k3sURL == "" {
		http.Error(w, "control-plane URL not configured (set KUSO_K3S_URL)", http.StatusServiceUnavailable)
		return
	}

	mergedLabels := nodejoin.MergeFactLabels(tok.Labels, req)
	// Apply the kuso. label namespace prefix on the wire so the new
	// node's kubelet boots with kuso-namespaced labels — matching the
	// label model the placement matcher already uses.
	prefixed := make(map[string]string, len(mergedLabels))
	for k, v := range mergedLabels {
		prefixed[kusoLabelPrefix+k] = v
	}
	nodeName := tok.NodeName
	if nodeName == "" {
		nodeName = req.Hostname
	}
	installCmd := nodejoin.BuildInstallCommand(k3sURL, k3sToken, prefixed, tok.NodeName)

	// Best-effort: stamp the joined node name on the row so the UI
	// shows "joined as foo" instead of "consumed". Failures don't
	// block the response — the kube node list is the source of truth.
	if nodeName != "" {
		if err := h.DB.MarkNodeBootstrapJoined(r.Context(), jti, nodeName); err != nil {
			h.Logger.Warn("mark bootstrap joined", "err", err, "jti", jti)
		}
	}

	h.Logger.Info("nodejoin: bootstrap token consumed",
		"jtiHash", db.HashJTI(jti)[:8], "hostname", req.Hostname, "arch", req.Arch,
		"cloud", req.CloudProvider, "from", fromIP)

	if h.Audit != nil {
		// Joining a node is the highest-privilege action this surface
		// can perform — produce a warn-level audit entry tied to the
		// minting user (tok.CreatedBy) since the bootstrap call itself
		// is unauthenticated. The from-IP + hostname + arch give an
		// incident responder enough to reconstruct what landed.
		h.Audit.Log(r.Context(), audit.Entry{
			User:     tok.CreatedBy,
			Severity: "warn",
			Action:   "node.bootstrap.joined",
			Resource: "node",
			Message:  fmt.Sprintf("node joined via bootstrap jtiHash=%s… from=%s hostname=%q arch=%q cloud=%q nodeName=%q", db.HashJTI(jti)[:8], fromIP, req.Hostname, req.Arch, req.CloudProvider, nodeName),
		})
	}

	// Only the install-command goes back in the response. The raw
	// k3s server token used to be in K3sToken; that's a long-lived
	// cluster-wide secret and a script error path could echo it to
	// the operator's terminal. The install-command already has the
	// token shell-escaped inside it, which is exactly what the
	// script needs to run.
	writeJSON(w, http.StatusOK, nodejoin.RegisterResponse{
		NodeName:       tok.NodeName,
		Labels:         mergedLabels,
		InstallCommand: installCmd,
	})
}

// publicBaseURL derives kuso's externally-reachable base URL.
//
// Preference order:
//   1. KUSO_PUBLIC_URL env (when set) — operator's source of truth.
//   2. X-Forwarded-{Proto,Host} headers when the request arrived from
//      a peer in KUSO_TRUSTED_PROXIES (operator-configured, fully
//      trusts the proxy to set host + scheme).
//   3. X-Forwarded-Proto from any peer (scheme-only) — see "Scheme
//      heuristic" below; never reads X-Forwarded-Host from untrusted
//      peers to avoid Host-spoofing.
//   4. r.TLS + r.Host as the last resort (direct caller, no proxy).
//
// # Scheme heuristic
//
// In the common install path, kuso-server sits behind Traefik (or any
// ingress controller) that terminates TLS and forwards plain HTTP to
// the pod. The operator hits https://kuso.example.com in their browser
// — but inside the cluster r.TLS is nil. Without KUSO_PUBLIC_URL or
// KUSO_TRUSTED_PROXIES set, the old code returned http:// in that
// configuration, which baked an http:// URL into the bootstrap script.
// The script's POST to that URL then got a 308 redirect from the
// ingress and curl -fsS treats 3xx as a failure (no -L flag).
//
// We now read X-Forwarded-Proto from any peer for the scheme decision
// only. The threat model accepts this: an attacker who can set
// X-Forwarded-Proto can change the script's KUSO_URL scheme, but they
// can't change the host (we still use r.Host for unauthenticated
// peers), and the only thing the script does with that URL is POST
// the bootstrap token back to the same kuso server. Misdirecting the
// POST to https://kuso.example.com when the operator wanted http://
// is harmless; misdirecting to an attacker's host would require
// setting X-Forwarded-Host, which we still gate behind the trusted-
// proxy check.
func publicBaseURL(r *http.Request) string {
	if v := strings.TrimSpace(os.Getenv("KUSO_PUBLIC_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	host := r.Host
	trustedProxy := peerIsTrustedProxy(remoteHost(r))
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		// Scheme can be read from any peer — see header doc above.
		scheme = proto
	}
	if trustedProxy {
		if forwarded := r.Header.Get("X-Forwarded-Host"); forwarded != "" {
			host = forwarded
		}
	}
	return scheme + "://" + host
}

// remoteHost extracts the IP portion of r.RemoteAddr — the connection
// peer, irrespective of any forwarded-for headers. Used as the input
// to peerIsTrustedProxy for the XFF trust gate.
func remoteHost(r *http.Request) string {
	host, _, err := splitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// clientIP is provided by auth.go (XFF-aware, trusted-proxy gated).
