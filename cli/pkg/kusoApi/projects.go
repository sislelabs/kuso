// v0.2 project/service/environment/addon API methods.
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

	// Service + addon DTOs land here from apiv1 so the CLI shares
	// the canonical wire shape with server-go. Fields appearing in
	// apiv1 but not in the old CLI shape (DisplayName, Command,
	// Domains, EnvVars, Scale, Sleep, Static, Buildpacks) are now
	// CLI-visible too — a long-overdue surface widening.
	//
	// SetEnvRequest stays a CLI-local type in env.go because its
	// callers build `[]map[string]any` rather than the typed
	// EnvVar slice; the wire shape matches either way (apiv1's
	// EnvVar carries the same json tags) and rewriting every
	// caller costs more than it earns.
	AddDomainRequest  = apiv1.AddDomainRequest
	SetEnvVarRequest  = apiv1.SetEnvVarRequest
	SecretRefBody     = apiv1.SecretRefBody
	UpdateAddonBackup = apiv1.UpdateAddonBackup
	AddonExternalSpec = apiv1.AddonExternalSpec
	EnvVar            = apiv1.EnvVar
	ServiceDomain     = apiv1.ServiceDomain
	ServiceRepoSpec   = apiv1.ServiceRepoSpec
	ServiceScale      = apiv1.ServiceScale
	ServiceSleep      = apiv1.ServiceSleep
	ServiceStatic     = apiv1.ServiceStatic
	ServiceBuildpacks = apiv1.ServiceBuildpacks
)

// Pointer helpers re-exported so existing call sites keep their
// kusoApi.BoolPtr / IntPtr / StringPtr names.
var (
	BoolPtr   = apiv1.BoolPtr
	IntPtr    = apiv1.IntPtr
	StringPtr = apiv1.StringPtr
)

// CreateServiceRequest and friends now alias the apiv1 wire shapes
// directly. Existing call sites that set Repo as a value-typed
// anonymous struct were migrated to the apiv1.ServiceRepoSpec
// pointer at the same time as this alias landed.
type (
	CreateServiceRequest  = apiv1.CreateServiceRequest
	ServiceImageSpec      = apiv1.ServiceImage
	CreateAddonRequest    = apiv1.CreateAddonRequest
	AddonExternalRequest  = apiv1.AddonExternalSpec
)

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

// PatchServiceDomain is the wire shape of one entry in the PATCH
// .../services/{s}.domains list. Same struct as apiv1.ServiceDomain
// but with the json:"host" / json:"tls" tags using strict (non-
// omitempty) emission so a {"host":"","tls":false} clear is
// distinguishable from "leave alone."
type PatchServiceDomain = apiv1.ServiceDomain

// PatchServiceRequest stays a CLI-local type until apiv1 grows the
// full server-side PatchServiceRequest surface (placement, volumes,
// scale, sleep, repo, previews — the domain shape is broader than
// the create path). The CLI only edits the small subset below.
type PatchServiceRequest struct {
	DisplayName *string              `json:"displayName,omitempty"`
	Port        *int32               `json:"port,omitempty"`
	Runtime     *string              `json:"runtime,omitempty"`
	Domains     *[]PatchServiceDomain `json:"domains,omitempty"`
	Internal      *bool                `json:"internal,omitempty"`
	PrivateEgress *bool                `json:"privateEgress,omitempty"`
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

// SetEnvVarSecretRefBody is the legacy alias for apiv1.SecretRefBody.
// Kept for one release so external import paths still resolve; new
// code should use SecretRefBody directly.
type SetEnvVarSecretRefBody = apiv1.SecretRefBody

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

// CreateEnvRequest mirrors the server's projects.CreateEnvRequest.
// Kept CLI-local rather than aliasing the server type because the
// server type lives in an internal package; the JSON wire-shape is
// the contract and matches field-for-field.
type CreateEnvRequest struct {
	Name         string `json:"name"`
	Branch       string `json:"branch"`
	HostOverride string `json:"host,omitempty"`
}

func (k *KusoClient) GetEnvironments(project string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + esc(project) + "/envs")
}

func (k *KusoClient) GetEnvironment(project, env string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + esc(project) + "/envs/" + env)
}

// AddEnvironment creates a custom environment (e.g. "staging", "qa")
// on a service. "production" and "pr-*" are reserved server-side. The
// new env inherits the service's envFromSecrets, addons, and port; the
// caller can override the host via req.HostOverride to point at a
// different DNS name than the auto-generated "<env>.<service>.<base>".
func (k *KusoClient) AddEnvironment(project, service string, req CreateEnvRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/projects/" + esc(project) + "/services/" + esc(service) + "/envs")
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

// UpdateAddonRequest aliases the apiv1 wire shape. The legacy
// nested-Backup field shape is preserved verbatim.
type UpdateAddonRequest = apiv1.UpdateAddonRequest

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

// ApplyConfig POSTs a kuso.yaml body to the config-as-code apply
// endpoint. dryRun adds ?dryRun=1 so the server returns the plan only
// and writes nothing. Body is sent as application/yaml. This is the
// config-as-code name for the same call Apply makes.
func (k *KusoClient) ApplyConfig(project string, body []byte, dryRun bool) (*resty.Response, error) {
	return k.Apply(project, body, dryRun)
}

// GetProjectSpec GETs the project's current live state reconstructed
// as a kuso.yaml document. Returns the raw YAML in resp.Body().
func (k *KusoClient) GetProjectSpec(project string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + esc(project) + "/spec")
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
