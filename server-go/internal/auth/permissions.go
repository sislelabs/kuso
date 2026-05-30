// Package auth — permission resolution + middleware.
//
// The roles → permissions table is the single source of truth for
// what a user can do. Endpoints declare the permission they need
// (services:write, sql:read, etc.) and the middleware checks the
// session against that list.
//
// We keep the table in code (not DB) on purpose: it's tied to the
// shape of the API, and an operator changing roles → perms via SQL
// would create silent privilege escalation. Custom roles can come
// later via a Role table that overrides this default.
package auth

import (
	"errors"
	"net/http"

	"kuso/server/internal/db"
)

// Permission is a typed string so accidental misuse (passing a perm
// where a role is expected) is a compile error.
type Permission string

// The matrix below is the entire set the server cares about. Add
// here, then wire into route declarations. Keep the names :-style
// (resource:action) so the UI can match prefixes for "show any
// settings:* affordance" checks.
const (
	// Instance-level permissions — admin-only. These are the perms baked
	// into the JWT by Compute(); a non-admin's JWT carries none of them.
	PermSettingsAdmin Permission = "settings:admin"
	PermSettingsRead  Permission = "settings:read"
	PermAuditRead     Permission = "audit:read"
	PermUserWrite     Permission = "user:write"
	PermBillingRead   Permission = "billing:read"
	PermSystemUpdate  Permission = "system:update"

	// Project-scoped permissions. NOT baked into the JWT — resolved
	// fresh per-request from the caller's effective role on the target
	// project (see PermsForProjectRole + requireProjectAccess). Listed
	// here as the canonical permission vocabulary the UI matches on.
	PermProjectWrite  Permission = "project:write"
	PermProjectRead   Permission = "project:read"
	PermServicesWrite Permission = "services:write"
	PermServicesRead  Permission = "services:read"
	PermSecretsWrite  Permission = "secrets:write" // set env vars (editor+)
	PermSecretsRead   Permission = "secrets:read"  // read env VALUES (admin only)
	PermShellExec     Permission = "shell:exec"    // pod shell / exec (admin only)
	PermSQLRead       Permission = "sql:read"      // SQL console / DB browser (admin only)
	PermAddonsWrite   Permission = "addons:write"
	PermAddonsRead    Permission = "addons:read"
)

// Compute returns the INSTANCE-level permission set baked into a
// principal's JWT. In role-system v2, only admins carry instance perms;
// viewer/editor get nothing here — their project access is resolved
// fresh per-request from PermsForProjectRole, not from the token.
//
// Pure function — easy to test without DB.
func Compute(t db.GroupTenancy) []string {
	if t.InstanceRole != db.InstanceRoleAdmin {
		// Non-admins carry NO instance-level perms. They authenticate,
		// but every project-scoped check re-resolves their effective
		// role from the DB against the specific project being accessed.
		return []string{}
	}
	// Admins get every instance perm. Project perms are granted
	// implicitly: requireProjectAccess + PermsForProjectRole treat an
	// admin as admin-on-every-project, and ProjectsAccessible returns
	// nil ("no filter — all projects").
	perms := []Permission{
		PermSettingsAdmin, PermSettingsRead, PermAuditRead, PermUserWrite,
		PermBillingRead, PermSystemUpdate,
	}
	out := make([]string, 0, len(perms))
	for _, p := range perms {
		out = append(out, string(p))
	}
	return out
}

// PermsForProjectRole returns the permission set a principal holds on a
// project given their EFFECTIVE role there (already resolved via
// ProjectRoleFor, which applies admin > override > inherited-instance).
// This is the project-scoped half of the matrix — resolved per request,
// never cached in the JWT.
//
//	viewer → read-only (project/services/addons read)
//	editor → + write (project/services/addons), + secrets:write (set env blind)
//	admin  → + secrets:read (env values), shell:exec, sql:read
//
// secrets:read / shell:exec / sql:read are the three "read arbitrary
// secret-bearing data" surfaces, deliberately admin-only.
func PermsForProjectRole(role db.ProjectRole) []string {
	set := map[Permission]struct{}{}
	add := func(p Permission) { set[p] = struct{}{} }

	switch role {
	case db.ProjectRoleAdmin:
		for _, p := range []Permission{
			PermProjectRead, PermServicesRead, PermAddonsRead,
			PermProjectWrite, PermServicesWrite, PermAddonsWrite, PermSecretsWrite,
			PermSecretsRead, PermShellExec, PermSQLRead,
		} {
			add(p)
		}
	case db.ProjectRoleEditor:
		for _, p := range []Permission{
			PermProjectRead, PermServicesRead, PermAddonsRead,
			PermProjectWrite, PermServicesWrite, PermAddonsWrite, PermSecretsWrite,
		} {
			add(p)
		}
	case db.ProjectRoleViewer:
		for _, p := range []Permission{
			PermProjectRead, PermServicesRead, PermAddonsRead,
		} {
			add(p)
		}
	default:
		return []string{}
	}

	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, string(p))
	}
	return out
}

// HasProjectPerm reports whether the effective project role grants want.
// Convenience over PermsForProjectRole for single-perm checks in
// project-scoped handlers (env read, shell, sql).
func HasProjectPerm(role db.ProjectRole, want Permission) bool {
	return Has(PermsForProjectRole(role), want)
}

// Has returns true when the session carries the requested permission.
// Tolerant of nil claims — middleware-less routes shouldn't call this,
// but if they do, no perms means no access.
func Has(perms []string, want Permission) bool {
	for _, p := range perms {
		if p == string(want) {
			return true
		}
	}
	return false
}

// HasAny is the OR variant — used for routes that accept either of
// two perms (e.g. "settings:admin OR settings:read" for endpoints
// that show settings UI but mask the secret).
func HasAny(perms []string, wants ...Permission) bool {
	for _, w := range wants {
		if Has(perms, w) {
			return true
		}
	}
	return false
}

// Require returns a middleware that 403s requests whose session
// doesn't carry want. The session is read from the context the
// bearer middleware sets — keeping a single context key for claims
// is the only way to avoid a "wait, which middleware ran first"
// debugging session a year from now.
func (i *Issuer) Require(want Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, ok := ClaimsFromContext(r.Context())
			if !ok {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if !Has(c.Permissions, want) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ProjectsAccessible returns the set of project names the session
// can see. Admins (settings:admin) bypass with a nil result, which
// callers interpret as "no filter — return all". Project-scoped users
// get the explicit list.
func ProjectsAccessible(t db.GroupTenancy) []string {
	if t.InstanceRole == db.InstanceRoleAdmin {
		return nil
	}
	out := make([]string, 0, len(t.ProjectMemberships))
	for _, m := range t.ProjectMemberships {
		out = append(out, m.Project)
	}
	return out
}

// ProjectRoleFor returns the principal's EFFECTIVE role on a specific
// project. Empty string means "no access / project invisible."
//
// Resolution (role-system v2):
//  1. Instance admin → admin on every project.
//  2. Otherwise, among the principal's grants applying to this project,
//     each grant's level = its explicit override, else the principal's
//     instance role (inherited), else viewer (an explicit grant always
//     confers at least read). Effective role = highest of those.
//  3. No applicable grant → "" (invisible).
//
// t.ProjectMemberships carries the already-resolved grants for this
// principal (the DB layer flattens ProjectGrant rows + the inherit/
// override + instance-role defaulting into m.Role before we get here),
// so this function stays a pure highest-wins pick.
func ProjectRoleFor(t db.GroupTenancy, project string) db.ProjectRole {
	if t.InstanceRole == db.InstanceRoleAdmin {
		return db.ProjectRoleAdmin
	}
	best := db.ProjectRole("")
	bestRank := 0
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
	for _, m := range t.ProjectMemberships {
		if m.Project != project {
			continue
		}
		if rk := rank(m.Role); rk > bestRank {
			bestRank = rk
			best = m.Role
		}
	}
	return best
}

// ErrPending is returned by the login flow when a user has been
// authenticated but has no group membership yet. Surfaced to the
// client with a custom status so the UI can route to /awaiting-access
// instead of looping back to /login.
var ErrPending = errors.New("auth: account pending admin approval")

// IsPending decides whether a user with this tenancy should be treated
// as "awaiting access" by login — routed to /awaiting-access instead of
// the dashboard. In role-system v2 a principal is pending when they have
// no usable access at all: not an admin, and no project grants AND no
// usable instance role.
//
// A viewer/editor instance role WITH at least one project grant is not
// pending. A viewer/editor with zero grants still sees nothing useful,
// so we treat them as pending too (they can authenticate but have no
// visible projects until an admin grants one).
func IsPending(t db.GroupTenancy) bool {
	if t.InstanceRole == db.InstanceRoleAdmin {
		return false
	}
	return len(t.ProjectMemberships) == 0
}
