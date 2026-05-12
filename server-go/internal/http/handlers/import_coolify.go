package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/sislelabs/kuso/coolify"

	"kuso/server/internal/addons"
	"kuso/server/internal/httpx"
	"kuso/server/internal/projects"
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
	Logger   *slog.Logger
	Projects *projects.Service
	Addons   *addons.Service
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

// CommitResponse summarises what the commit did. Counters drive the
// success toast; Skipped/Errors carry per-row reasons for the result
// table the wizard renders after commit.
type CommitResponse struct {
	ProjectsCreated int            `json:"projectsCreated"`
	ServicesCreated int            `json:"servicesCreated"`
	AddonsCreated   int            `json:"addonsCreated"`
	EnvVarsCreated  int            `json:"envVarsCreated"`
	Skipped         []CommitDetail `json:"skipped"`
	Errors          []CommitDetail `json:"errors"`
}

type CommitDetail struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

func (h *ImportCoolifyHandler) Commit(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if h.Projects == nil || h.Addons == nil {
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
	resp := h.applyCommit(ctx, c, inv, picked)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// applyCommit walks the inventory, keeps the rows whose UUID was
// ticked in the wizard, and provisions kuso resources for them.
// Mirrors cli/cmd/kusoCli/migrate.go's applyMigration but writes
// through the in-process services instead of the HTTP API — same
// shape, half the latency, and the audit log records this as a
// single admin action rather than dozens of un-correlated calls.
func (h *ImportCoolifyHandler) applyCommit(ctx context.Context, c *coolify.Client, inv *coolify.Inventory, picked map[string]struct{}) CommitResponse {
	out := CommitResponse{}
	// Group picked items by Coolify project name. Two distinct Coolify
	// projects can slugify to the same name; assignCoolifySlugs
	// disambiguates with a numeric suffix.
	byCoolifyName := map[string][]coolify.Item{}
	coolifyOrder := []string{}
	for _, it := range inv.Items {
		uuid := coolifyItemUUID(it)
		if uuid == "" {
			continue
		}
		if _, ok := picked[uuid]; !ok {
			continue
		}
		kind := coolifyItemKind(it)
		if it.Verdict.Action != "migrate" {
			out.Skipped = append(out.Skipped, CommitDetail{
				Kind:   kind,
				Name:   it.Name,
				Reason: "verdict=" + it.Verdict.Action + " (preview classifier blocked import)",
			})
			continue
		}
		if it.ProjectName == "" {
			out.Skipped = append(out.Skipped, CommitDetail{Kind: kind, Name: it.Name, Reason: "no Coolify project"})
			continue
		}
		if _, ok := byCoolifyName[it.ProjectName]; !ok {
			coolifyOrder = append(coolifyOrder, it.ProjectName)
		}
		byCoolifyName[it.ProjectName] = append(byCoolifyName[it.ProjectName], it)
	}
	slugFor := assignCoolifySlugs(coolifyOrder)

	for _, coolifyName := range coolifyOrder {
		projectSlug := slugFor[coolifyName]
		items := byCoolifyName[coolifyName]
		// Find a git-backed app to seed defaultRepo. Without one the
		// project would need its repo wired in the UI later anyway; we
		// could allow it, but the CLI parity check requires a repo so
		// we skip here too. The Skipped row carries the reason so the
		// wizard renders it inline.
		var defaultRepoURL, defaultBranch string
		for _, it := range items {
			if it.App != nil && it.App.GitRepository != "" {
				defaultRepoURL = "https://github.com/" + strings.TrimSuffix(it.App.GitRepository, ".git")
				defaultBranch = it.App.GitBranch
				break
			}
		}
		if defaultRepoURL == "" {
			out.Skipped = append(out.Skipped, CommitDetail{
				Kind: "project", Name: coolifyName,
				Reason: "no git-backed app — kuso project requires a defaultRepo",
			})
			continue
		}

		_, err := h.Projects.Create(ctx, projects.CreateProjectRequest{
			Name: projectSlug,
			DefaultRepo: &projects.CreateProjectRepoSpec{
				URL:           defaultRepoURL,
				DefaultBranch: defaultBranch,
			},
		})
		switch {
		case err == nil:
			out.ProjectsCreated++
		case errors.Is(err, projects.ErrConflict):
			// Already exists — fine, fall through to children.
		default:
			out.Errors = append(out.Errors, CommitDetail{Kind: "project", Name: projectSlug, Reason: err.Error()})
			continue
		}

		// Apps → services + envs.
		for _, it := range items {
			if it.App == nil {
				continue
			}
			svcSlug := coolifyServiceSlug(it.App)
			svcReq := projects.CreateServiceRequest{
				Name:    svcSlug,
				Runtime: runtimeForBuildPack(it.App.BuildPack),
				Port:    int32(parseFirstPort(it.App.PortsExposes)),
				Repo: &projects.CreateServiceRepo{
					URL:  "https://github.com/" + strings.TrimSuffix(it.App.GitRepository, ".git"),
					Path: it.App.BaseDirectory,
				},
			}
			if _, err := h.Projects.AddService(ctx, projectSlug, svcReq); err != nil {
				if !errors.Is(err, projects.ErrConflict) {
					out.Errors = append(out.Errors, CommitDetail{Kind: "service", Name: projectSlug + "/" + svcSlug, Reason: err.Error()})
					continue
				}
			} else {
				out.ServicesCreated++
			}

			envs, err := c.ListApplicationEnvs(ctx, it.App.UUID)
			if err != nil {
				out.Errors = append(out.Errors, CommitDetail{Kind: "env", Name: projectSlug + "/" + svcSlug, Reason: "list envs: " + err.Error()})
				continue
			}
			envVars := make([]projects.EnvVar, 0, len(envs))
			for _, e := range envs {
				if e.IsCoolify {
					continue
				}
				envVars = append(envVars, projects.EnvVar{Name: e.Key, Value: e.EffectiveValue()})
			}
			if len(envVars) == 0 {
				continue
			}
			if err := h.Projects.SetEnv(ctx, projectSlug, svcSlug, envVars); err != nil {
				out.Errors = append(out.Errors, CommitDetail{Kind: "env", Name: projectSlug + "/" + svcSlug, Reason: err.Error()})
				continue
			}
			out.EnvVarsCreated += len(envVars)
		}

		// Databases → addons.
		for _, it := range items {
			if it.Database == nil {
				continue
			}
			kind := coolify.AddonKindFromCoolify(it.Database.DatabaseType)
			if kind == "" {
				out.Skipped = append(out.Skipped, CommitDetail{Kind: "addon", Name: it.Name, Reason: "unsupported database type " + it.Database.DatabaseType})
				continue
			}
			addonName := slugifyName(it.Database.Name)
			if _, err := h.Addons.Add(ctx, projectSlug, addons.CreateAddonRequest{Name: addonName, Kind: kind}); err != nil {
				if !errors.Is(err, addons.ErrConflict) {
					out.Errors = append(out.Errors, CommitDetail{Kind: "addon", Name: projectSlug + "/" + addonName, Reason: err.Error()})
					continue
				}
			} else {
				out.AddonsCreated++
			}
		}
	}
	if h.Logger != nil {
		h.Logger.Info("coolify import committed",
			"projects", out.ProjectsCreated,
			"services", out.ServicesCreated,
			"addons", out.AddonsCreated,
			"envVars", out.EnvVarsCreated,
			"skipped", len(out.Skipped),
			"errors", len(out.Errors),
		)
	}
	return out
}

// coolifyItemUUID returns the Coolify UUID of an item if known. Used
// to match wizard-ticked rows against the freshly fetched inventory.
func coolifyItemUUID(it coolify.Item) string {
	switch {
	case it.App != nil:
		return it.App.UUID
	case it.Database != nil:
		return it.Database.UUID
	case it.Service != nil:
		return it.Service.UUID
	}
	return ""
}

// coolifyItemKind picks a human label for the resource kind. Used
// only in error/skip rows.
func coolifyItemKind(it coolify.Item) string {
	switch {
	case it.App != nil:
		return "application"
	case it.Database != nil:
		return "database"
	case it.Service != nil:
		return "service"
	}
	return "unknown"
}

// assignCoolifySlugs maps Coolify project names to kuso project slugs,
// disambiguating collisions with a numeric suffix. Mirrors the CLI's
// assignKusoSlugs verbatim.
func assignCoolifySlugs(names []string) map[string]string {
	taken := map[string]bool{}
	out := map[string]string{}
	for _, n := range names {
		base := slugifyName(n)
		slug := base
		i := 2
		for taken[slug] {
			slug = fmt.Sprintf("%s-%d", base, i)
			i++
		}
		taken[slug] = true
		out[n] = slug
	}
	return out
}

// coolifyServiceSlug derives a kuso service slug from a Coolify app.
// Same pattern as the CLI: take the basename of the git repo so a
// service is "todo-api", not "biznesguys/todo-api:main-abc123".
func coolifyServiceSlug(a *coolify.Application) string {
	if a == nil {
		return ""
	}
	repo := strings.TrimSuffix(a.GitRepository, ".git")
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		repo = repo[i+1:]
	}
	if repo == "" {
		repo = a.Name
	}
	return slugifyName(repo)
}

// runtimeForBuildPack maps a Coolify BuildPack to a kuso runtime.
// Unknown maps to "dockerfile" — the safest default since every
// Coolify app needs *some* build, and a Dockerfile is the lowest
// common denominator.
func runtimeForBuildPack(bp string) string {
	switch strings.ToLower(bp) {
	case "nixpacks":
		return "nixpacks"
	case "static":
		return "static"
	case "dockerfile", "":
		return "dockerfile"
	default:
		return "dockerfile"
	}
}

// parseFirstPort takes a comma-separated Coolify port list and
// returns the first numeric value, or 0 on no match.
func parseFirstPort(s string) int {
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// Coolify can emit "3000:3000" — take the listening side
		// (left), not the container side.
		if i := strings.Index(part, ":"); i > 0 {
			part = part[:i]
		}
		var n int
		if _, err := fmt.Sscanf(part, "%d", &n); err == nil && n > 0 && n < 65536 {
			return n
		}
	}
	return 0
}

var slugifyRE = regexp.MustCompile(`[^a-z0-9-]+`)

// slugifyName: lowercase, non-[a-z0-9-] → "-", strip leading/trailing
// "-", clamp to 63 chars (kube DNS label max).
func slugifyName(s string) string {
	s = strings.ToLower(s)
	s = slugifyRE.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 63 {
		s = s[:63]
	}
	return s
}
