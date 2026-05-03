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
	PermSettingsAdmin     Permission = "settings:admin"
	PermSettingsRead      Permission = "settings:read"
	PermAuditRead         Permission = "audit:read"
	PermUserWrite         Permission = "user:write"
	PermBillingRead       Permission = "billing:read"
	PermProjectWrite      Permission = "project:write"
	PermProjectRead       Permission = "project:read"
	PermServicesWrite     Permission = "services:write"
	PermServicesRead      Permission = "services:read"
	PermSecretsWrite      Permission = "secrets:write"
	PermSecretsRead       Permission = "secrets:read"
	PermSQLRead           Permission = "sql:read"
	PermAddonsWrite       Permission = "addons:write"
	PermAddonsRead        Permission = "addons:read"
	PermSystemUpdate      Permission = "system:update"
)

// Compute returns the union permission set for a given tenancy.
// Pure function — easy to test without DB.
func Compute(t db.GroupTenancy) []string {
	set := map[Permission]struct{}{}
	add := func(p Permission) { set[p] = struct{}{} }

	switch t.InstanceRole {
	case db.InstanceRoleAdmin:
		// Admins get everything plus the system-level perms; project
		// perms inherit from project membership, but admins also see
		// every project (handled in the API list filter, not here).
		for _, p := range []Permission{
			PermSettingsAdmin, PermSettingsRead, PermAuditRead, PermUserWrite,
			PermBillingRead, PermProjectWrite, PermProjectRead,
			PermServicesWrite, PermServicesRead, PermSecretsWrite, PermSecretsRead,
			PermSQLRead, PermAddonsWrite, PermAddonsRead, PermSystemUpdate,
		} {
			add(p)
		}
	case db.InstanceRoleBilling:
		add(PermBillingRead)
		add(PermSettingsRead)
	case db.InstanceRoleViewer:
		add(PermSettingsRead)
		add(PermProjectRead)
		add(PermServicesRead)
		add(PermAddonsRead)
	case db.InstanceRoleMember:
		// No instance-level perms; perms come from project membership.
	case db.InstanceRolePending, "":
		// No perms at all — user can authenticate but can't see
		// anything until an admin assigns them a group.
		return []string{}
	}

	for _, m := range t.ProjectMemberships {
		switch m.Role {
		case db.ProjectRoleOwner:
			add(PermProjectWrite)
			add(PermProjectRead)
			add(PermServicesWrite)
			add(PermServicesRead)
			add(PermSecretsWrite)
			add(PermSecretsRead)
			add(PermSQLRead)
			add(PermAddonsWrite)
			add(PermAddonsRead)
		case db.ProjectRoleDeployer:
			add(PermProjectRead)
			add(PermServicesWrite)
			add(PermServicesRead)
			add(PermSecretsRead)
			add(PermAddonsRead)
		case db.ProjectRoleViewer:
			add(PermProjectRead)
			add(PermServicesRead)
			add(PermAddonsRead)
		}
	}

	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, string(p))
	}
	return out
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

// ProjectRoleFor returns the user's effective role on a specific
// project, taking instance-admin into account (admins are always
// owners). Empty string means "no access."
func ProjectRoleFor(t db.GroupTenancy, project string) db.ProjectRole {
	if t.InstanceRole == db.InstanceRoleAdmin {
		return db.ProjectRoleOwner
	}
	for _, m := range t.ProjectMemberships {
		if m.Project == project {
			return m.Role
		}
	}
	return ""
}

// ErrPending is returned by the login flow when a user has been
// authenticated but has no group membership yet. Surfaced to the
// client with a custom status so the UI can route to /awaiting-access
// instead of looping back to /login.
var ErrPending = errors.New("auth: account pending admin approval")

// IsPending decides whether a user with this tenancy should be
// treated as "awaiting access" by login. Empty memberships AND
// pending instance role both qualify.
func IsPending(t db.GroupTenancy) bool {
	if t.InstanceRole == db.InstanceRoleAdmin {
		return false
	}
	if t.InstanceRole == db.InstanceRolePending {
		return true
	}
	if t.InstanceRole == db.InstanceRoleMember && len(t.ProjectMemberships) == 0 {
		return true
	}
	return false
}
