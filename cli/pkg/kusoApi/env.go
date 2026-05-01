// Env vars + secrets API client.
//
// Env vars live on KusoService.spec.envVars (plain or secretKeyRef-shaped).
// Secrets live in a real Kubernetes Secret named <project>-<service>-secrets,
// auto-mounted via the env's envFromSecrets list. Secret VALUES are never
// returned over the wire — only keys.

package kusoApi

import "github.com/go-resty/resty/v2"

type SetEnvRequest struct {
	EnvVars []map[string]any `json:"envVars"`
}

type SetSecretRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func (k *KusoClient) GetEnv(project, service string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + project + "/services/" + service + "/env")
}

func (k *KusoClient) SetEnv(project, service string, req SetEnvRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/projects/" + project + "/services/" + service + "/env")
}

func (k *KusoClient) ListSecrets(project, service string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + project + "/services/" + service + "/secrets")
}

func (k *KusoClient) SetSecret(project, service string, req SetSecretRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/projects/" + project + "/services/" + service + "/secrets")
}

func (k *KusoClient) UnsetSecret(project, service, key string) (*resty.Response, error) {
	return k.client.Delete("/api/projects/" + project + "/services/" + service + "/secrets/" + key)
}
