// ssh_keys.go — client methods for the SSH key library that backs the
// "Add node" flow: list stored keys, add one (either server-generated
// or operator-pasted), and delete by id. All admin-gated server-side.
//
// CreateSSHKeyRequest mirrors the handler's accepted body: either
// {name, generate: true} for a fresh server-side ed25519 keypair, or
// {name, publicKey, privateKey} to import an existing pair. The
// response always carries the public half + fingerprint.

package kusoApi

import "github.com/go-resty/resty/v2"

// SSHKey is one stored key. The private half is never returned on the
// wire (the server strips it), so PrivateKey is absent from reads.
// Mirrors the read shape of server-go internal/db.SSHKey.
type SSHKey struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	PublicKey   string `json:"publicKey"`
	Fingerprint string `json:"fingerprint"`
	CreatedAt   string `json:"createdAt"`
}

// CreateSSHKeyRequest is the body for POST /api/ssh-keys. Set Generate
// for a server-side ed25519 keypair; otherwise supply both PublicKey
// and PrivateKey to import an existing pair.
type CreateSSHKeyRequest struct {
	Name       string `json:"name"`
	Generate   bool   `json:"generate,omitempty"`
	PublicKey  string `json:"publicKey,omitempty"`
	PrivateKey string `json:"privateKey,omitempty"`
}

// ListSSHKeys returns every stored key (without private bytes).
// Admin-gated. Response: a bare JSON array of SSHKey.
func (k *KusoClient) ListSSHKeys() (*resty.Response, error) {
	return k.client.Get("/api/ssh-keys")
}

// CreateSSHKey adds a key. Response (201): the created SSHKey, which on
// a fresh generate also carries the public half + fingerprint to copy
// into a remote authorized_keys.
func (k *KusoClient) CreateSSHKey(req CreateSSHKeyRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/ssh-keys")
}

// DeleteSSHKey removes a stored key by id. 204 on success, 404 when the
// id is unknown.
func (k *KusoClient) DeleteSSHKey(id string) (*resty.Response, error) {
	return k.client.Delete("/api/ssh-keys/" + esc(id))
}
