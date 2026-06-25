// Invite API client. Mirrors the admin half of /api/invites — mint,
// list, revoke. All three require user:write (instance admin); the
// public lookup/redeem routes are intentionally not exposed here (the
// CLI user is already authenticated, so they have no use for them).
package kusoApi

import "github.com/go-resty/resty/v2"

// CreateInviteRequest mirrors the server's createInviteRequest. All
// fields are optional. ExpiresIn is a Go duration string ("168h"); a
// multi-use invite (MaxUses > 1) REQUIRES ExpiresIn. MaxUses defaults
// to 1 server-side when zero.
type CreateInviteRequest struct {
	GroupID      string `json:"groupId,omitempty"`
	InstanceRole string `json:"instanceRole,omitempty"`
	ExpiresIn    string `json:"expiresIn,omitempty"`
	MaxUses      int    `json:"maxUses,omitempty"`
	Note         string `json:"note,omitempty"`
}

// CreateInvite mints a fresh invite. POST /api/invites. The 201 body is
// {invite:{...}, url:"https://.../invite/<token>"}.
func (k *KusoClient) CreateInvite(req CreateInviteRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/invites")
}

// ListInvites returns all invites (newest-first), each annotated with a
// public url field. GET /api/invites.
func (k *KusoClient) ListInvites() (*resty.Response, error) {
	return k.client.Get("/api/invites")
}

// RevokeInvite soft-revokes an invite by id. DELETE /api/invites/{id}.
func (k *KusoClient) RevokeInvite(id string) (*resty.Response, error) {
	return k.client.Delete("/api/invites/" + esc(id))
}
