// Group / RBAC API client. Mirrors /api/groups CRUD (GroupsHandler),
// membership management, tenancy, and the v2 instance-role setter
// (GrantsHandler). Most routes require user:write (instance admin);
// ListGroupMembers requires settings:admin specifically.
package kusoApi

import "github.com/go-resty/resty/v2"

// GroupRequest is the create/update body — name + optional description.
type GroupRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ProjectMembership is one entry in a group's tenancy. Role is
// admin|editor|viewer.
type ProjectMembership struct {
	Project string `json:"project"`
	Role    string `json:"role"`
}

// GroupTenancy is the {instanceRole, projectMemberships} shape that
// GetGroupTenancy returns and SetGroupTenancy replaces atomically.
type GroupTenancy struct {
	InstanceRole       string              `json:"instanceRole"`
	ProjectMemberships []ProjectMembership `json:"projectMemberships"`
}

// SetGroupInstanceRoleRequest sets a group's instance role.
// Role is admin|editor|viewer, or "" to clear.
type SetGroupInstanceRoleRequest struct {
	Role string `json:"role"`
}

// ListGroups returns the admin group list ([]{id,name,description}).
// GET /api/groups.
func (k *KusoClient) ListGroups() (*resty.Response, error) {
	return k.client.Get("/api/groups")
}

// CreateGroup mints a new group. POST /api/groups.
func (k *KusoClient) CreateGroup(req GroupRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/groups")
}

// UpdateGroup replaces a group's name + description. PUT /api/groups/{id}.
func (k *KusoClient) UpdateGroup(id string, req GroupRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Put("/api/groups/" + esc(id))
}

// DeleteGroup removes a group. DELETE /api/groups/{id}.
func (k *KusoClient) DeleteGroup(id string) (*resty.Response, error) {
	return k.client.Delete("/api/groups/" + esc(id))
}

// ListGroupMembers returns {data:[{id,username,email}]} for a group.
// GET /api/groups/{id}/members. Requires settings:admin.
func (k *KusoClient) ListGroupMembers(id string) (*resty.Response, error) {
	return k.client.Get("/api/groups/" + esc(id) + "/members")
}

// AddGroupMember attaches a user to a group (idempotent).
// POST /api/groups/{id}/members/{userId}.
func (k *KusoClient) AddGroupMember(groupID, userID string) (*resty.Response, error) {
	return k.client.Post("/api/groups/" + esc(groupID) + "/members/" + esc(userID))
}

// RemoveGroupMember detaches a user from a group (idempotent).
// DELETE /api/groups/{id}/members/{userId}.
func (k *KusoClient) RemoveGroupMember(groupID, userID string) (*resty.Response, error) {
	return k.client.Delete("/api/groups/" + esc(groupID) + "/members/" + esc(userID))
}

// GetGroupTenancy reads a group's instanceRole + projectMemberships.
// GET /api/groups/{id}/tenancy.
func (k *KusoClient) GetGroupTenancy(id string) (*resty.Response, error) {
	return k.client.Get("/api/groups/" + esc(id) + "/tenancy")
}

// SetGroupTenancy replaces a group's tenancy atomically.
// PUT /api/groups/{id}/tenancy.
func (k *KusoClient) SetGroupTenancy(id string, req GroupTenancy) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Put("/api/groups/" + esc(id) + "/tenancy")
}

// SetGroupInstanceRole sets just the group's instance role, preserving
// project memberships. PUT /api/groups/{id}/instance-role.
func (k *KusoClient) SetGroupInstanceRole(id string, req SetGroupInstanceRoleRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Put("/api/groups/" + esc(id) + "/instance-role")
}
