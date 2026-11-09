// User-management API client. Mirrors the admin /api/users surface:
// list (AdminHandler), full CRUD + password set (UsersHandler), and the
// v2 instance-role setter (GrantsHandler). All routes require user:write
// (instance admin); the server gates them and returns 403 otherwise.
package kusoApi

import "github.com/go-resty/resty/v2"

// CreateUserRequest mirrors the server's createUserRequest.
type CreateUserRequest struct {
	Username  string `json:"username"`
	Email     string `json:"email"`
	Password  string `json:"password"`
	FirstName string `json:"firstName,omitempty"`
	LastName  string `json:"lastName,omitempty"`
	RoleID    string `json:"roleId,omitempty"`
	IsActive  *bool  `json:"isActive,omitempty"`
}

// SetUserInstanceRoleRequest sets a user's direct instance role.
// Role is admin|editor|viewer, or "" to clear (inherit from groups).
type SetUserInstanceRoleRequest struct {
	Role string `json:"role"`
}

// ListUsers returns the admin user summary list ([]{id,username,email,
// firstName,lastName,isActive,role,instanceRole}). GET /api/users.
func (k *KusoClient) ListUsers() (*resty.Response, error) {
	return k.client.Get("/api/users")
}

// GetUser fetches one user by id. GET /api/users/id/{id}.
func (k *KusoClient) GetUser(id string) (*resty.Response, error) {
	return k.client.Get("/api/users/id/" + esc(id))
}

// CreateUser mints a new local account. POST /api/users.
func (k *KusoClient) CreateUser(req CreateUserRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/users")
}

// DeleteUser removes a user. DELETE /api/users/id/{id}.
func (k *KusoClient) DeleteUser(id string) (*resty.Response, error) {
	return k.client.Delete("/api/users/id/" + esc(id))
}

// SetUserPassword overwrites a user's password (admin path, no
// current-password check). PUT /api/users/id/{id}/password.
func (k *KusoClient) SetUserPassword(id, password string) (*resty.Response, error) {
	k.client.SetBody(map[string]string{"password": password})
	return k.client.Put("/api/users/id/" + esc(id) + "/password")
}

// SetUserInstanceRole sets the user's v2 instance role.
// PUT /api/users/{id}/instance-role.
func (k *KusoClient) SetUserInstanceRole(id string, req SetUserInstanceRoleRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Put("/api/users/" + esc(id) + "/instance-role")
}

// UserAccessSource names where access comes from: "direct" (the user
// row) or "group" (GroupID/GroupName set).
type UserAccessSource struct {
	Via          string `json:"via"`
	GroupID      string `json:"groupId,omitempty"`
	GroupName    string `json:"groupName,omitempty"`
	Role         string `json:"role"`
	RoleOverride string `json:"roleOverride,omitempty"`
}

// UserAccess mirrors GET /api/users/{id}/access.
type UserAccess struct {
	UserID              string             `json:"userId"`
	Username            string             `json:"username"`
	InstanceRole        string             `json:"instanceRole"`
	InstanceRoleSources []UserAccessSource `json:"instanceRoleSources"`
	Groups              []struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		InstanceRole string `json:"instanceRole"`
	} `json:"groups"`
	AllProjects bool `json:"allProjects"`
	Projects    []struct {
		Project string             `json:"project"`
		Role    string             `json:"role"`
		Sources []UserAccessSource `json:"sources"`
	} `json:"projects"`
	Pending bool `json:"pending"`
}

// GetUserAccess returns a user's effective access and where each part
// comes from. GET /api/users/{id}/access.
func (k *KusoClient) GetUserAccess(id string) (*resty.Response, error) {
	return k.client.Get("/api/users/" + esc(id) + "/access")
}
