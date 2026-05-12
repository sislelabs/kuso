// v0.2 project/service/environment/addon API methods. See docs/REDESIGN.md.
//
// All requests bind to /api/projects/... and rely on the bearer token wired
// up by KusoClient.Init / SetApiUrl. Note: KusoClient.client is a
// *resty.Request, not a Client — call .SetBody() then .Post(path).

package kusoApi

import (
	"net/url"

	"github.com/go-resty/resty/v2"

	apiv1 "github.com/sislelabs/kuso/api/apiv1"
)

// esc escapes a single URL path segment. Project/service identifiers
// are kuso-validated DNS labels so they're effectively safe, but
// host names (with `:`, `.`), env var names, secret keys, and addon
// names are user-controlled — an unescaped `/` or `?` would either
// pierce the next path segment or graft a phantom query string onto
// the request. PathEscape leaves `:` alone (legal in a path segment
// per RFC 3986 §3.3), so a `host:port` domain still round-trips.
func esc(s string) string { return url.PathEscape(s) }

// Project DTOs are now defined in kuso/api/v1. The aliases below
// keep every existing kusoApi.CreateProjectRequest call site
// compiling while the shared module becomes the single source of
// truth for wire shape. A future field add in api/v1/projects.go
// flows through to both server and CLI automatically.
type (
	CreateProjectRequest  = apiv1.CreateProjectRequest
	UpdateProjectRequest  = apiv1.UpdateProjectRequest
	RepoRef               = apiv1.RepoRef
	GitHubInstallationRef = apiv1.GitHubInstallationRef
	PreviewsSettings      = apiv1.PreviewsSettings
	PreviewsPatch         = apiv1.PreviewsPatch
)

// Pointer helpers re-exported so existing call sites keep their
// kusoApi.BoolPtr / IntPtr / StringPtr names.
var (
	BoolPtr   = apiv1.BoolPtr
	IntPtr    = apiv1.IntPtr
	StringPtr = apiv1.StringPtr
)

type CreateServiceRequest struct {
	Name string `json:"name"`
	Repo struct {
		URL  string `json:"url,omitempty"`
		Path string `json:"path,omitempty"`
	} `json:"repo"`
	Runtime string `json:"runtime,omitempty"`
	Port    int    `json:"port,omitempty"`
	// Image is required when runtime=image — points at an existing
	// registry image instead of building from a repo. Server stamps
	// it onto the env CR at create time so the chart pulls
	// directly without spinning a kaniko Job. Other runtimes
	// ignore this field.
	Image *ServiceImageSpec `json:"image,omitempty"`
}

// ServiceImageSpec mirrors projects.ServiceImageSpec on the server —
// duplicated here so the CLI doesn't have to import server-go/.
// Repository is the full reference up to (not including) the tag,
// e.g. "ghcr.io/foo/bar". Tag defaults to "latest" server-side
// when empty, with the usual mutable-tag caveat (next redeploy
// won't observe a new image until the user changes the tag).
type ServiceImageSpec struct {
	Repository string `json:"repository,omitempty"`
	Tag        string `json:"tag,omitempty"`
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
	return k.client.Get("/api/projects/" + esc(name))
}

func (k *KusoClient) CreateProject(req CreateProjectRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/projects")
}

func (k *KusoClient) DeleteProject(name string) (*resty.Response, error) {
	return k.client.Delete("/api/projects/" + esc(name))
}

func (k *KusoClient) UpdateProject(name string, req UpdateProjectRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Patch("/api/projects/" + esc(name))
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
	return k.client.Get("/api/projects/" + esc(project) + "/services")
}

func (k *KusoClient) GetService(project, service string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + esc(project) + "/services/" + esc(service))
}

func (k *KusoClient) AddService(project string, req CreateServiceRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/projects/" + esc(project) + "/services")
}

func (k *KusoClient) DeleteService(project, service string) (*resty.Response, error) {
	return k.client.Delete("/api/projects/" + esc(project) + "/services/" + esc(service))
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
	return k.client.Patch("/api/projects/" + esc(project) + "/services/" + esc(service))
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
	return k.client.Post("/api/projects/" + esc(project) + "/services/" + esc(service) + "/domains")
}

// RemoveDomain drops a single host. 404 when the host wasn't there.
func (k *KusoClient) RemoveDomain(project, service, host string) (*resty.Response, error) {
	return k.client.Delete("/api/projects/" + esc(project) + "/services/" + esc(service) + "/domains/" + esc(host))
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
	return k.client.Put("/api/projects/" + esc(project) + "/services/" + esc(service) + "/env-vars/" + esc(name))
}

// UnsetEnvVar removes a single env var by name. 404 when absent.
func (k *KusoClient) UnsetEnvVar(project, service, name string) (*resty.Response, error) {
	return k.client.Delete("/api/projects/" + esc(project) + "/services/" + esc(service) + "/env-vars/" + esc(name))
}

// Environments

func (k *KusoClient) GetEnvironments(project string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + esc(project) + "/envs")
}

func (k *KusoClient) GetEnvironment(project, env string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + esc(project) + "/envs/" + env)
}

func (k *KusoClient) DeleteEnvironment(project, env string) (*resty.Response, error) {
	return k.client.Delete("/api/projects/" + esc(project) + "/envs/" + env)
}

// Addons

func (k *KusoClient) GetAddonsForProject(project string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + esc(project) + "/addons")
}

func (k *KusoClient) AddAddon(project string, req CreateAddonRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/projects/" + esc(project) + "/addons")
}

func (k *KusoClient) DeleteAddon(project, addon string) (*resty.Response, error) {
	// ?confirm=<addon> is required server-side. Always pass it; the
	// CLI gets its own typed-confirm prompt one layer up.
	return k.client.Delete("/api/projects/" + esc(project) + "/addons/" + esc(addon) + "?confirm=" + esc(addon))
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
	return k.client.Patch("/api/projects/" + esc(project) + "/addons/" + esc(addon))
}

// ResyncExternalAddon re-mirrors an external addon's source Secret
// into its <name>-conn. Use after the upstream credentials rotated.
func (k *KusoClient) ResyncExternalAddon(project, addon string) (*resty.Response, error) {
	return k.client.Post("/api/projects/" + esc(project) + "/addons/" + esc(addon) + "/resync-external")
}

// ResyncInstanceAddon re-provisions the per-project DB on a shared
// instance addon and rotates the password.
func (k *KusoClient) ResyncInstanceAddon(project, addon string) (*resty.Response, error) {
	return k.client.Post("/api/projects/" + esc(project) + "/addons/" + esc(addon) + "/resync-instance")
}

// RepairAddonPassword fixes the helm-chart password drift bug by
// ALTERing the postgres user inside the running pod to match the
// current conn secret value.
func (k *KusoClient) RepairAddonPassword(project, addon string) (*resty.Response, error) {
	return k.client.Post("/api/projects/" + esc(project) + "/addons/" + esc(addon) + "/repair-password")
}

// Apply posts a kuso.yml body to the server's config-as-code endpoint.
// dryRun=true returns a Plan without writing; false applies it.
func (k *KusoClient) Apply(project string, yamlBody []byte, dryRun bool) (*resty.Response, error) {
	url := "/api/projects/" + esc(project) + "/apply"
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
	return k.client.Get("/api/projects/" + esc(project))
}

// ListRevisions returns the most recent revisions for one resource
// (kind ∈ {service, environment, addon, cron}). Limit defaults to 50
// on the server, hard cap 200.
func (k *KusoClient) ListRevisions(project, kind, name string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + esc(project) + "/revisions/" + esc(kind) + "/" + esc(name))
}

// GetRevision returns one revision (full snapshot included).
func (k *KusoClient) GetRevision(project, id string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + esc(project) + "/revisions/" + esc(id))
}

// RevertRevision replays a stored snapshot through the matching
// update endpoint. Currently only kind=service is supported by the
// server; addon/env revert returns 501.
func (k *KusoClient) RevertRevision(project, id string) (*resty.Response, error) {
	return k.client.Post("/api/projects/" + esc(project) + "/revisions/" + esc(id) + "/revert")
}

// RenameService clones the service + envs under newName and deletes
// the old. Implemented server-side as clone-then-delete because kube
// CRD names are immutable.
func (k *KusoClient) RenameService(project, service, newName string) (*resty.Response, error) {
	k.client.SetBody(map[string]string{"newName": newName})
	return k.client.Post("/api/projects/" + esc(project) + "/services/" + esc(service) + "/rename")
}

// ExportProject streams a tar.gz of the project's spec (project +
// services + envs + addons + secrets) over a single response. The
// caller gets the raw bytes back via resp.Body().
func (k *KusoClient) ExportProject(project string) (*resty.Response, error) {
	return k.client.Post("/api/projects/" + esc(project) + "/export")
}

// ImportProject uploads a tarball produced by ExportProject. policy
// is one of "error" (default), "rename", "overwrite" — see the
// server's ExportHandler.Import for semantics.
func (k *KusoClient) ImportProject(tarball []byte, policy string) (*resty.Response, error) {
	k.client.SetBody(tarball)
	k.client.SetHeader("Content-Type", "application/gzip")
	path := "/api/projects/import"
	if policy != "" {
		path += "?policy=" + policy
	}
	return k.client.Post(path)
}
