// CLI client for the v0.10 pull-mode node-join surface. Mirrors
// server-go/internal/http/handlers/node_bootstrap.go.
//
//   POST   /api/kubernetes/nodes/bootstrap-tokens
//   GET    /api/kubernetes/nodes/bootstrap-tokens
//   DELETE /api/kubernetes/nodes/bootstrap-tokens/{jti}

package kusoApi

import (
	"github.com/go-resty/resty/v2"
)

// MintNodeBootstrapTokenRequest matches the server's mint endpoint.
// Labels are bare keys (no kuso. prefix) — the server prefixes them
// when patching the joined node.
type MintNodeBootstrapTokenRequest struct {
	Labels     map[string]string `json:"labels,omitempty"`
	NodeName   string            `json:"nodeName,omitempty"`
	TTLSeconds int               `json:"ttlSeconds,omitempty"`
}

// MintNodeBootstrapToken returns the freshly-minted token + the curl
// one-liner the operator pastes on the new VM.
func (k *KusoClient) MintNodeBootstrapToken(req MintNodeBootstrapTokenRequest) (*resty.Response, error) {
	return k.client.SetBody(req).Post("/api/kubernetes/nodes/bootstrap-tokens")
}

// ListPendingNodeBootstrapTokens returns unconsumed tokens (for the
// `kuso node pending` view).
func (k *KusoClient) ListPendingNodeBootstrapTokens() (*resty.Response, error) {
	return k.client.Get("/api/kubernetes/nodes/bootstrap-tokens")
}

// RevokeNodeBootstrapToken cancels a pending token. The argument is
// a prefix or the full hash of the jti — NOT the cleartext token,
// since the server only stores the hash. The mint endpoint returns
// `jtiPrefix` and the list endpoint returns both `jtiPrefix` and
// `jtiHash`; either is acceptable.
func (k *KusoClient) RevokeNodeBootstrapToken(jtiHashOrPrefix string) (*resty.Response, error) {
	return k.client.Delete("/api/kubernetes/nodes/bootstrap-tokens/" + esc(jtiHashOrPrefix))
}
