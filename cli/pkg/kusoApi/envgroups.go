// Env-group API client. Mirrors /api/projects/{p}/env-groups — the
// "clone every service+addon into a new named env" surface (staging,
// client-demo, ...). Distinct from `environment` (single-service env)
// and `env` (env-vars). Server CRUD existed since v0.16.x but had no CLI.
package kusoApi

import "github.com/go-resty/resty/v2"

// CreateEnvGroupRequest mirrors projects.CreateEnvGroupRequest. AddonPolicy
// is keyed by short addon name; values are "fresh" (own empty datastore,
// the default) or "shared" (reuse production's). Omitted addons default
// to fresh so a typo never silently shares production data.
type CreateEnvGroupRequest struct {
	Name        string            `json:"name"`
	AddonPolicy map[string]string `json:"addonPolicy,omitempty"`
}

func (k *KusoClient) ListEnvGroups(project string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + esc(project) + "/env-groups")
}

func (k *KusoClient) GetEnvGroup(project, name string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + esc(project) + "/env-groups/" + esc(name))
}

func (k *KusoClient) CreateEnvGroup(project string, req CreateEnvGroupRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/projects/" + esc(project) + "/env-groups")
}

// DeleteEnvGroup tears down a non-production env group. The server gates
// the delete behind ?confirm=<name> to acknowledge data loss (matches the
// addon-delete contract), so we pass it explicitly.
func (k *KusoClient) DeleteEnvGroup(project, name string) (*resty.Response, error) {
	return k.client.Delete("/api/projects/" + esc(project) + "/env-groups/" + esc(name) + "?confirm=" + esc(name))
}
