// Instance-shared secrets — admin-only env vars auto-attached to
// every service in every project via envFromSecrets.

package kusoApi

import "github.com/go-resty/resty/v2"

func (k *KusoClient) ListInstanceSecrets() (*resty.Response, error) {
	return k.client.Get("/api/instance-secrets")
}

// SetInstanceSecretRequest is the PUT body. Unlike the project shared
// secret it has no force: the server has no shadow check here.
type SetInstanceSecretRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func (k *KusoClient) SetInstanceSecret(req SetInstanceSecretRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Put("/api/instance-secrets")
}

func (k *KusoClient) UnsetInstanceSecret(key string) (*resty.Response, error) {
	return k.client.Delete("/api/instance-secrets/" + esc(key))
}

// Instance addons — Model 2 shared DB servers. Built on top of
// instance secrets but surfaced as a separate /api/instance-addons
// resource so the UI/CLI can show host + user without leaking the
// password.

type RegisterInstanceAddonRequest struct {
	Name string `json:"name"`
	DSN  string `json:"dsn"`
}

func (k *KusoClient) ListInstanceAddons() (*resty.Response, error) {
	return k.client.Get("/api/instance-addons")
}

func (k *KusoClient) RegisterInstanceAddon(req RegisterInstanceAddonRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Put("/api/instance-addons")
}

func (k *KusoClient) UnregisterInstanceAddon(name string) (*resty.Response, error) {
	return k.client.Delete("/api/instance-addons/" + esc(name))
}
