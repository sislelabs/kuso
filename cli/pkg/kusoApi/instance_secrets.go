// Instance-shared secrets — admin-only env vars auto-attached to
// every service in every project via envFromSecrets.

package kusoApi

import "github.com/go-resty/resty/v2"

func (k *KusoClient) ListInstanceSecrets() (*resty.Response, error) {
	return k.client.Get("/api/instance-secrets")
}

func (k *KusoClient) SetInstanceSecret(req SetSharedSecretRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Put("/api/instance-secrets")
}

func (k *KusoClient) UnsetInstanceSecret(key string) (*resty.Response, error) {
	return k.client.Delete("/api/instance-secrets/" + key)
}
