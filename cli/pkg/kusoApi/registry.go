// Project registry credentials for private runtime=image pulls. Routes:
//   GET    /api/projects/{p}/registry-credentials             → list (no passwords)
//   POST   /api/projects/{p}/registry-credentials             → login/upsert
//   DELETE /api/projects/{p}/registry-credentials/{registry}  → logout

package kusoApi

import "github.com/go-resty/resty/v2"

type RegistryLoginRequest struct {
	Registry string `json:"registry"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type RegistryCredential struct {
	Registry   string `json:"registry"`
	Username   string `json:"username"`
	SecretName string `json:"secretName"`
	UpdatedAt  string `json:"updatedAt,omitempty"`
}

func (k *KusoClient) RegistryLogin(project string, req RegistryLoginRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/projects/" + esc(project) + "/registry-credentials")
}

func (k *KusoClient) ListRegistryCredentials(project string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + esc(project) + "/registry-credentials")
}

func (k *KusoClient) RegistryLogout(project, registry string) (*resty.Response, error) {
	return k.client.Delete("/api/projects/" + esc(project) + "/registry-credentials/" + esc(registry))
}
