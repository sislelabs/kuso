// v0.2 project/service/environment/addon API methods. See docs/REDESIGN.md.
//
// All requests bind to /api/projects/... and rely on the bearer token wired
// up by KusoClient.Init / SetApiUrl. Note: KusoClient.client is a
// *resty.Request, not a Client — call .SetBody() then .Post(path).

package kusoApi

import (
	"github.com/go-resty/resty/v2"
)

type CreateProjectRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	BaseDomain  string `json:"baseDomain,omitempty"`
	Namespace   string `json:"namespace,omitempty"`
	DefaultRepo struct {
		URL           string `json:"url"`
		DefaultBranch string `json:"defaultBranch,omitempty"`
	} `json:"defaultRepo"`
	GitHub *struct {
		InstallationID int64 `json:"installationId,omitempty"`
	} `json:"github,omitempty"`
	Previews struct {
		Enabled bool `json:"enabled"`
		TTLDays int  `json:"ttlDays,omitempty"`
	} `json:"previews"`
}

// UpdateProjectRequest mirrors the server's pointer-field shape so a
// caller can express "leave this field alone" (omit) vs. "set to zero"
// (send the zero value). Use Bool / Int / String helpers to build
// pointer literals tersely.
type UpdateProjectRequest struct {
	Description *string `json:"description,omitempty"`
	BaseDomain  *string `json:"baseDomain,omitempty"`
	DefaultRepo *struct {
		URL           string `json:"url,omitempty"`
		DefaultBranch string `json:"defaultBranch,omitempty"`
	} `json:"defaultRepo,omitempty"`
	GitHub *struct {
		InstallationID int64 `json:"installationId,omitempty"`
	} `json:"github,omitempty"`
	Previews *struct {
		Enabled *bool `json:"enabled,omitempty"`
		TTLDays *int  `json:"ttlDays,omitempty"`
	} `json:"previews,omitempty"`
}

func BoolPtr(b bool) *bool       { return &b }
func IntPtr(i int) *int          { return &i }
func StringPtr(s string) *string { return &s }

type CreateServiceRequest struct {
	Name string `json:"name"`
	Repo struct {
		URL  string `json:"url,omitempty"`
		Path string `json:"path,omitempty"`
	} `json:"repo"`
	Runtime string `json:"runtime,omitempty"`
	Port    int    `json:"port,omitempty"`
}

type CreateAddonRequest struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Version string `json:"version,omitempty"`
	Size    string `json:"size,omitempty"`
	HA      bool   `json:"ha,omitempty"`
}

// Projects

func (k *KusoClient) GetProjects() (*resty.Response, error) {
	return k.client.Get("/api/projects")
}

func (k *KusoClient) GetProject(name string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + name)
}

func (k *KusoClient) CreateProject(req CreateProjectRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/projects")
}

func (k *KusoClient) DeleteProject(name string) (*resty.Response, error) {
	return k.client.Delete("/api/projects/" + name)
}

func (k *KusoClient) UpdateProject(name string, req UpdateProjectRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Patch("/api/projects/" + name)
}

// RawPost sends a raw byte body with an explicit content-type. Used by
// the restore flow where the body is a SQLite file, not JSON.
func (k *KusoClient) RawPost(path string, body []byte, contentType string) (*resty.Response, error) {
	k.client.SetHeader("Content-Type", contentType)
	k.client.SetBody(body)
	return k.client.Post(path)
}

// Services

func (k *KusoClient) GetServices(project string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + project + "/services")
}

func (k *KusoClient) GetService(project, service string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + project + "/services/" + service)
}

func (k *KusoClient) AddService(project string, req CreateServiceRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/projects/" + project + "/services")
}

func (k *KusoClient) DeleteService(project, service string) (*resty.Response, error) {
	return k.client.Delete("/api/projects/" + project + "/services/" + service)
}

// Environments

func (k *KusoClient) GetEnvironments(project string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + project + "/envs")
}

func (k *KusoClient) GetEnvironment(project, env string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + project + "/envs/" + env)
}

func (k *KusoClient) DeleteEnvironment(project, env string) (*resty.Response, error) {
	return k.client.Delete("/api/projects/" + project + "/envs/" + env)
}

// Addons

func (k *KusoClient) GetAddonsForProject(project string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + project + "/addons")
}

func (k *KusoClient) AddAddon(project string, req CreateAddonRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/projects/" + project + "/addons")
}

func (k *KusoClient) DeleteAddon(project, addon string) (*resty.Response, error) {
	return k.client.Delete("/api/projects/" + project + "/addons/" + addon)
}
