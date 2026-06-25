// Project-grant API client. Mirrors /api/projects/{p}/grants — per-project
// RBAC (grant a user or group access at admin|editor|viewer). Server CRUD
// existed but was UI-only; this makes project access scriptable.
package kusoApi

import "github.com/go-resty/resty/v2"

// AddGrantRequest grants project access to a user XOR a group. Role is
// admin|editor|viewer, or "" to inherit the grantee's instance role.
type AddGrantRequest struct {
	UserID  string `json:"userId,omitempty"`
	GroupID string `json:"groupId,omitempty"`
	Role    string `json:"role,omitempty"`
}

func (k *KusoClient) ListGrants(project string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + esc(project) + "/grants")
}

func (k *KusoClient) AddGrant(project string, req AddGrantRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/projects/" + esc(project) + "/grants")
}

func (k *KusoClient) RemoveGrant(project, grantID string) (*resty.Response, error) {
	return k.client.Delete("/api/projects/" + esc(project) + "/grants/" + esc(grantID))
}
