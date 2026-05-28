// Env vars + secrets API client.
//
// Env vars live on KusoService.spec.envVars (plain or secretKeyRef-shaped).
// Secrets live in a real Kubernetes Secret named <project>-<service>-secrets,
// auto-mounted via the env's envFromSecrets list. Secret VALUES are never
// returned over the wire — only keys.

package kusoApi

import "github.com/go-resty/resty/v2"

// SetEnvRequest stays a CLI-local type with a loose
// []map[string]any envVars slice — that's the shape the existing
// CLI callers build. apiv1's typed EnvVar uses the same JSON tags
// so the wire round-trip matches; we just don't force every
// caller through the typed constructor.
type SetEnvRequest struct {
	EnvVars []map[string]any `json:"envVars"`
}

type SetSecretRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	// Optional: scope this secret to one environment. Empty means
	// "shared" — applies to every env of the service.
	Env string `json:"env,omitempty"`
	// Force=true bypasses the server-side shadow check that warns when
	// this service-scoped value would override a project-shared value
	// of the same key. CLI maps this to --force.
	Force bool `json:"force,omitempty"`
}

func (k *KusoClient) GetEnv(project, service string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + esc(project) + "/services/" + esc(service) + "/env")
}

func (k *KusoClient) SetEnv(project, service string, req SetEnvRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/projects/" + esc(project) + "/services/" + esc(service) + "/env")
}

// GetSharedEnvKeys returns the available keys (grouped by source
// secret) + the service's current subscription. Body shape:
//
//	{ "subscribed": ["KEY1", ...], "sources":
//	  [ { "secret": "<project>-shared", "keys": [...] }, ... ] }
//
// Post-v0.16.11 every service has an explicit subscription (the
// startup migration seeds existing services from their currently-
// mounted keys), so the subscribed list is always authoritative.
func (k *KusoClient) GetSharedEnvKeys(project, service string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + esc(project) + "/services/" + esc(service) + "/shared-env-keys")
}

// SetSharedEnvKeys replaces the subscription list. Empty (non-nil)
// slice = subscribe to nothing.
func (k *KusoClient) SetSharedEnvKeys(project, service string, keys []string) (*resty.Response, error) {
	if keys == nil {
		keys = []string{}
	}
	k.client.SetBody(map[string]any{"keys": keys})
	return k.client.Put("/api/projects/" + esc(project) + "/services/" + esc(service) + "/shared-env-keys")
}

// envQuery returns "?env=<name>" or "" — kept inline rather than using
// resty's QueryParam to avoid leaking state into later requests on the
// shared client.
func envQuery(env string) string {
	if env == "" {
		return ""
	}
	return "?env=" + env
}

func (k *KusoClient) ListSecrets(project, service, env string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + esc(project) + "/services/" + esc(service) + "/secrets" + envQuery(env))
}

func (k *KusoClient) SetSecret(project, service string, req SetSecretRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/projects/" + esc(project) + "/services/" + esc(service) + "/secrets")
}

func (k *KusoClient) UnsetSecret(project, service, key, env string) (*resty.Response, error) {
	return k.client.Delete("/api/projects/" + esc(project) + "/services/" + esc(service) + "/secrets/" + esc(key) + envQuery(env))
}
