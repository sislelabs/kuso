// Project-level shared secrets — env vars auto-mounted into every
// service in the project via envFromSecrets. Routes:
//   GET    /api/projects/{p}/shared-secrets        → key list (no values)
//   PUT    /api/projects/{p}/shared-secrets        → upsert {key, value}
//   DELETE /api/projects/{p}/shared-secrets/{key}  → remove key

package kusoApi

import "github.com/go-resty/resty/v2"

type SetSharedSecretRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func (k *KusoClient) ListSharedSecrets(project string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + esc(project) + "/shared-secrets")
}

func (k *KusoClient) SetSharedSecret(project string, req SetSharedSecretRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Put("/api/projects/" + esc(project) + "/shared-secrets")
}

func (k *KusoClient) UnsetSharedSecret(project, key string) (*resty.Response, error) {
	return k.client.Delete("/api/projects/" + esc(project) + "/shared-secrets/" + esc(key))
}
