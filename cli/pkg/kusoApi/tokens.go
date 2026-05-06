// API tokens. Long-lived JWTs the user can issue for CI / scripts so
// they don't have to embed username/password.

package kusoApi

import "github.com/go-resty/resty/v2"

type CreateTokenRequest struct {
	Name      string `json:"name"`
	ExpiresAt string `json:"expiresAt"` // RFC3339
}

func (k *KusoClient) ListTokens() (*resty.Response, error) {
	return k.client.Get("/api/tokens/my")
}

func (k *KusoClient) CreateToken(req CreateTokenRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/tokens/my")
}

func (k *KusoClient) DeleteToken(id string) (*resty.Response, error) {
	return k.client.Delete("/api/tokens/my/" + esc(id))
}
