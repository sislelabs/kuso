// Package handlers — authz helpers shared across handlers.
//
// Two gates, used in slightly different shapes:
//
//  1. requireAdmin / requirePerm — instance-level permission checks.
//     A handler that mutates platform state (users, groups, roles, nodes,
//     tokens, etc.) calls one of these at the top to 401/403 callers
//     without the perm. The gate writes the response, so on a false
//     return the handler must just return.
//
//  2. requireProjectAccess — project-scoped ownership check. A handler
//     that operates on a {project} path param (and optionally a
//     {service}, {env}, {addon} child) calls this to confirm the caller
//     has a ProjectMembership on that project. Admins bypass.
//
// Both gates resolve tenancy from the DB on every request; we accept the
// SQLite hit because it's the only way to be sure of an up-to-date role
// after a group change. Callers pass *db.DB; handlers that don't have it
// (a few legacy ones) skip the check, which is the same behaviour the
// rest of the codebase already had.
package handlers

import (
	"context"
	"log/slog"
	"net/http"

	"kuso/server/internal/auth"
	"kuso/server/internal/db"
	"kuso/server/internal/projects"
)

// requirePerm 401s requests with no claims and 403s requests whose
// claims don't carry want. Returns false when the response was already
// written, so the handler should `return` immediately.
func requirePerm(w http.ResponseWriter, r *http.Request, want auth.Permission) bool {
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	if !auth.Has(claims.Permissions, want) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	return true
}

// requireAdmin is the common shorthand. settings:admin is the perm an
// instance admin always carries.
func requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	return requirePerm(w, r, auth.PermSettingsAdmin)
}

// AdminOnly is the middleware form of requireAdmin — wrap a chi.Group
// with it to gate every route inside in one place. Cuts the
// "did I forget the gate on this method" footgun that produced the
// audit-handler / notifications-feed regressions. Exported so
// router.go (different package) can reuse the same middleware.
func AdminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(w, r) {
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireUserWrite gates user/group/role/token mutations. user:write is
// granted to instance admins. Kept separate from settings:admin so the
// matrix can later split (e.g. a "user manager" role that doesn't see
// billing).
func requireUserWrite(w http.ResponseWriter, r *http.Request) bool {
	return requirePerm(w, r, auth.PermUserWrite)
}

// requireProjectAccess confirms the caller has at least minRole on the
// named project. Admins always pass. On false the response is already
// written.
//
// dbConn may be nil — handlers wired before the tenancy table existed
// pass nil and the function falls back to the legacy "any authenticated
// user" behaviour. Once every handler has *db.DB we can flip this to
// fail-closed.
func requireProjectAccess(
	ctx context.Context, w http.ResponseWriter,
	dbConn *db.DB, project string, minRole db.ProjectRole,
) bool {
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	// Admins see everything.
	if auth.Has(claims.Permissions, auth.PermSettingsAdmin) {
		return true
	}
	if dbConn == nil {
		// Fail closed: an unwired handler is a security bug, not a
		// feature gate. The previous fail-open behaviour let any JWT
		// bypass project-membership checks on any handler that pre-
		// dated the DB plumbing — exploitable today. Logging the call
		// site so the operator notices in production.
		slog.Default().Error("requireProjectAccess called with nil DB — failing closed",
			"project", project, "user", claims.UserID)
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	tenancy, err := dbConn.ListUserTenancyCached(ctx, claims.UserID)
	if err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	role := auth.ProjectRoleFor(tenancy, project)
	if role == "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	if !roleAtLeast(role, minRole) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	return true
}

// callerCanReadSecrets reports whether the request's caller may see env
// var VALUES on the named project. In role-system v2 this is admin-only
// (effective project role == admin, which instance admins always have).
// Editors can write env vars but must not read existing values, so env
// read endpoints mask values when this returns false.
//
// Fail-closed: any resolution error → false (mask).
func callerCanReadSecrets(ctx context.Context, dbConn *db.DB, project string) bool {
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok {
		return false
	}
	if auth.Has(claims.Permissions, auth.PermSettingsAdmin) {
		return true // instance admin → admin on every project
	}
	if dbConn == nil {
		return false
	}
	tenancy, err := dbConn.ListUserTenancyCached(ctx, claims.UserID)
	if err != nil {
		return false
	}
	return auth.HasProjectPerm(auth.ProjectRoleFor(tenancy, project), auth.PermSecretsRead)
}

// callerCanRunSQL reports whether the request's caller may use the SQL
// browser (list tables / run SELECT) against the project's databases.
// Admin-only in v2 — a SELECT can read any secret-bearing app table.
// Fail-closed.
func callerCanRunSQL(ctx context.Context, dbConn *db.DB, project string) bool {
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok {
		return false
	}
	if auth.Has(claims.Permissions, auth.PermSettingsAdmin) {
		return true
	}
	if dbConn == nil {
		return false
	}
	tenancy, err := dbConn.ListUserTenancyCached(ctx, claims.UserID)
	if err != nil {
		return false
	}
	return auth.HasProjectPerm(auth.ProjectRoleFor(tenancy, project), auth.PermSQLRead)
}

// maskEnvValues returns a copy of the env-var slice with every Value
// replaced by a sentinel, used when the caller may write but not read
// env values. Names + valueFrom refs are preserved so the editor UI can
// still show which keys exist and write new values blind.
func maskEnvValues(in []projects.EnvVar) []projects.EnvVar {
	out := make([]projects.EnvVar, len(in))
	for i, e := range in {
		out[i] = e
		if e.Value != "" {
			out[i].Value = envMaskSentinel
		}
	}
	return out
}

// envMaskSentinel is what editors see instead of a secret value. Chosen
// so it's visibly a mask, not a plausible real value.
const envMaskSentinel = "••••••••"

// roleAtLeast returns true when have grants at least the want level.
// admin > editor > viewer.
func roleAtLeast(have, want db.ProjectRole) bool {
	rank := func(r db.ProjectRole) int {
		switch r {
		case db.ProjectRoleAdmin:
			return 3
		case db.ProjectRoleEditor:
			return 2
		case db.ProjectRoleViewer:
			return 1
		}
		return 0
	}
	return rank(have) >= rank(want)
}
