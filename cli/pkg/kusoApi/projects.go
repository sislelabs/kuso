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
	// AlwaysOn=true overrides every per-service sleep config so all
	// services in this project run with scale-to-zero disabled.
	AlwaysOn *bool `json:"alwaysOn,omitempty"`
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
	Name             string                `json:"name"`
	Kind             string                `json:"kind"`
	Version          string                `json:"version,omitempty"`
	Size             string                `json:"size,omitempty"`
	HA               bool                  `json:"ha,omitempty"`
	External         *AddonExternalRequest `json:"external,omitempty"`
	UseInstanceAddon string                `json:"useInstanceAddon,omitempty"`
}

// AddonExternalRequest tells the server to skip provisioning and
// mirror an existing kube Secret as the addon's <name>-conn secret.
// SecretKeys is an optional allowlist; empty mirrors every key.
type AddonExternalRequest struct {
	SecretName string   `json:"secretName"`
	SecretKeys []string `json:"secretKeys,omitempty"`
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
// the restore flow where the body is a SQLite file, not JSON, and by
// trigger-style endpoints that don't take a body at all.
//
// resty rejects SetBody(nil) for a typed-nil []byte with
// "unsupported 'Body' type/value", so we explicitly clear the body
// when the caller passes nil. Empty []byte{} would also work but nil
// is what most callers reach for first.
func (k *KusoClient) RawPost(path string, body []byte, contentType string) (*resty.Response, error) {
	k.client.SetHeader("Content-Type", contentType)
	if body == nil {
		k.client.Body = nil
	} else {
		k.client.SetBody(body)
	}
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

// PatchServiceDomain mirrors the server's ServiceDomain shape on the
// PATCH path. Kept inline here so the CLI doesn't have to import the
// server types — the server is permissive about extra fields, so an
// older CLI talking to a newer server still works.
type PatchServiceDomain struct {
	Host string `json:"host"`
	TLS  bool   `json:"tls"`
}

// PatchServiceRequest is the partial-update body for PATCH /api/
// projects/{p}/services/{s}. Pointer fields distinguish "leave alone"
// from "set to zero". Only fields the CLI actually edits are listed
// — placement / volumes / scale stay UI-only for now.
type PatchServiceRequest struct {
	DisplayName *string              `json:"displayName,omitempty"`
	Port        *int32               `json:"port,omitempty"`
	Runtime     *string              `json:"runtime,omitempty"`
	Domains     *[]PatchServiceDomain `json:"domains,omitempty"`
	Internal    *bool                `json:"internal,omitempty"`
}

// PatchService applies a partial update to a service spec. Mirrors
// the UI's Settings → Save flow.
//
// For per-row edits (add one domain, set one env var), prefer the
// delta methods below — PatchService is whole-list replacement and
// races under concurrent edits.
func (k *KusoClient) PatchService(project, service string, req PatchServiceRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Patch("/api/projects/" + project + "/services/" + service)
}

// AddDomainRequest mirrors the server-side projects.AddDomainRequest.
type AddDomainRequest struct {
	Host string `json:"host"`
	TLS  bool   `json:"tls"`
}

// AddDomain appends a single domain to a service. The server applies
// the change under a per-service lock so concurrent adds don't race.
// 409 on a duplicate host with the same TLS flag.
func (k *KusoClient) AddDomain(project, service string, req AddDomainRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/projects/" + project + "/services/" + service + "/domains")
}

// RemoveDomain drops a single host. 404 when the host wasn't there.
func (k *KusoClient) RemoveDomain(project, service, host string) (*resty.Response, error) {
	return k.client.Delete("/api/projects/" + project + "/services/" + service + "/domains/" + host)
}

// SetEnvVarRequest mirrors the server-side projects.SetEnvVarRequest.
// Exactly one of Value or SecretRef must be set.
type SetEnvVarRequest struct {
	Value     string                  `json:"value,omitempty"`
	SecretRef *SetEnvVarSecretRefBody `json:"secretRef,omitempty"`
}

type SetEnvVarSecretRefBody struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

// SetEnvVar adds or overwrites a single env var by name. Idempotent.
func (k *KusoClient) SetEnvVar(project, service, name string, req SetEnvVarRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Put("/api/projects/" + project + "/services/" + service + "/env-vars/" + name)
}

// UnsetEnvVar removes a single env var by name. 404 when absent.
func (k *KusoClient) UnsetEnvVar(project, service, name string) (*resty.Response, error) {
	return k.client.Delete("/api/projects/" + project + "/services/" + service + "/env-vars/" + name)
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

// UpdateAddonBackup is the partial-update body for an addon's backup
// schedule. Mirrors the server's UpdateBackupPatch — pointer fields
// distinguish "leave alone" from "set". Schedule="" disables the
// cronjob entirely (chart drops the resource).
type UpdateAddonBackup struct {
	Schedule      *string `json:"schedule,omitempty"`
	RetentionDays *int    `json:"retentionDays,omitempty"`
}

// UpdateAddonRequest mirrors the server's partial-update body. Same
// pointer-field convention. Only the knobs the CLI actually edits
// today (size, version, HA, storageSize, database, backup) are
// listed; the chart accepts more fields server-side.
type UpdateAddonRequest struct {
	Version     *string             `json:"version,omitempty"`
	Size        *string             `json:"size,omitempty"`
	HA          *bool               `json:"ha,omitempty"`
	StorageSize *string             `json:"storageSize,omitempty"`
	Database    *string             `json:"database,omitempty"`
	Backup      *UpdateAddonBackup  `json:"backup,omitempty"`
}

// UpdateAddon PATCHes an addon's spec. Used by `kuso addon-backup
// schedule` to enable / change / disable the per-addon CronJob.
func (k *KusoClient) UpdateAddon(project, addon string, req UpdateAddonRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Patch("/api/projects/" + project + "/addons/" + addon)
}

// ResyncExternalAddon re-mirrors an external addon's source Secret
// into its <name>-conn. Use after the upstream credentials rotated.
func (k *KusoClient) ResyncExternalAddon(project, addon string) (*resty.Response, error) {
	return k.client.Post("/api/projects/" + project + "/addons/" + addon + "/resync-external")
}

// ResyncInstanceAddon re-provisions the per-project DB on a shared
// instance addon and rotates the password.
func (k *KusoClient) ResyncInstanceAddon(project, addon string) (*resty.Response, error) {
	return k.client.Post("/api/projects/" + project + "/addons/" + addon + "/resync-instance")
}

// RepairAddonPassword fixes the helm-chart password drift bug by
// ALTERing the postgres user inside the running pod to match the
// current conn secret value.
func (k *KusoClient) RepairAddonPassword(project, addon string) (*resty.Response, error) {
	return k.client.Post("/api/projects/" + project + "/addons/" + addon + "/repair-password")
}

// Apply posts a kuso.yml body to the server's config-as-code endpoint.
// dryRun=true returns a Plan without writing; false applies it.
func (k *KusoClient) Apply(project string, yamlBody []byte, dryRun bool) (*resty.Response, error) {
	url := "/api/projects/" + project + "/apply"
	if dryRun {
		url += "?dryRun=1"
	}
	k.client.SetBody(yamlBody)
	k.client.SetHeader("Content-Type", "application/yaml")
	return k.client.Post(url)
}

// GetProjectFull returns the project rollup (Describe) — project +
// services + envs in one call.
func (k *KusoClient) GetProjectFull(project string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + project)
}
