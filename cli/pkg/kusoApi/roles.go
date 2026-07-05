// roles.go — RBAC role CRUD against /api/roles.
//
// The slim list (id/name/description) is served by the admin handler at
// GET /api/roles; the permission-inlined shape and the write verbs live
// on the roles handler (GET /api/roles/full, POST /api/roles,
// PUT/DELETE /api/roles/{id}). All are admin/user-write gated server-side.

package kusoApi

import "github.com/go-resty/resty/v2"

// RolePermission is one {resource, action} grant on a role. Mirrors
// server-side db.PermissionInput.
type RolePermission struct {
	Resource string `json:"resource"`
	Action   string `json:"action"`
}

// RoleRequest is the create/update body accepted by the roles handler.
type RoleRequest struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Permissions []RolePermission `json:"permissions"`
}

// ListRoles returns the slim role list ({id,name,description}).
func (k *KusoClient) ListRoles() (*resty.Response, error) {
	return k.client.Get("/api/roles")
}

// ListRolesFull returns every role with its permissions inlined.
func (k *KusoClient) ListRolesFull() (*resty.Response, error) {
	return k.client.Get("/api/roles/full")
}

// CreateRole creates a role. Server mints the id and echoes it back.
func (k *KusoClient) CreateRole(req RoleRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/roles")
}

// UpdateRole replaces a role's name/description/permissions. A shrink in
// the permission set invalidates JWTs for users holding the role.
func (k *KusoClient) UpdateRole(id string, req RoleRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Put("/api/roles/" + esc(id))
}

// DeleteRole removes a role by id.
func (k *KusoClient) DeleteRole(id string) (*resty.Response, error) {
	return k.client.Delete("/api/roles/" + esc(id))
}
