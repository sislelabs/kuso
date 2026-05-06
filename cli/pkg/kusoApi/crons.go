// KusoCron API client. Mirrors /api/projects/{p}/services/{s}/crons.
package kusoApi

import "github.com/go-resty/resty/v2"

type CreateCronRequest struct {
	Name                  string   `json:"name"`
	Schedule              string   `json:"schedule"`
	Command               []string `json:"command"`
	Suspend               bool     `json:"suspend,omitempty"`
	ConcurrencyPolicy     string   `json:"concurrencyPolicy,omitempty"`
	ActiveDeadlineSeconds int      `json:"activeDeadlineSeconds,omitempty"`
}

// CronImage mirrors the helm chart's image{repository,tag,pullPolicy}
// shape. Used for kind=command project-scoped crons.
type CronImage struct {
	Repository string `json:"repository"`
	Tag        string `json:"tag,omitempty"`
	PullPolicy string `json:"pullPolicy,omitempty"`
}

// CreateProjectCronRequest covers kind=http and kind=command crons —
// the standalone variants exposed by /api/projects/{p}/crons. Service-
// attached crons stay on the per-service Add path that re-uses the
// parent service's image and envFromSecrets.
type CreateProjectCronRequest struct {
	Name                  string     `json:"name"`
	DisplayName           string     `json:"displayName,omitempty"`
	Kind                  string     `json:"kind"`
	Schedule              string     `json:"schedule"`
	URL                   string     `json:"url,omitempty"`
	Image                 *CronImage `json:"image,omitempty"`
	Command               []string   `json:"command,omitempty"`
	Suspend               bool       `json:"suspend,omitempty"`
	ConcurrencyPolicy     string     `json:"concurrencyPolicy,omitempty"`
	ActiveDeadlineSeconds int        `json:"activeDeadlineSeconds,omitempty"`
}

// UpdateProjectCronRequest is the partial-update body for project-
// scoped crons. Pointer fields distinguish "leave alone" from
// "set to zero".
type UpdateProjectCronRequest struct {
	DisplayName           *string    `json:"displayName,omitempty"`
	Schedule              *string    `json:"schedule,omitempty"`
	Suspend               *bool      `json:"suspend,omitempty"`
	URL                   *string    `json:"url,omitempty"`
	Image                 *CronImage `json:"image,omitempty"`
	Command               []string   `json:"command,omitempty"`
	ConcurrencyPolicy     *string    `json:"concurrencyPolicy,omitempty"`
	ActiveDeadlineSeconds *int       `json:"activeDeadlineSeconds,omitempty"`
}

type UpdateCronRequest struct {
	Schedule              *string  `json:"schedule,omitempty"`
	Command               []string `json:"command,omitempty"`
	Suspend               *bool    `json:"suspend,omitempty"`
	ConcurrencyPolicy     *string  `json:"concurrencyPolicy,omitempty"`
	ActiveDeadlineSeconds *int     `json:"activeDeadlineSeconds,omitempty"`
}

func (k *KusoClient) ListCrons(project string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + esc(project) + "/crons")
}

func (k *KusoClient) ListCronsForService(project, service string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + esc(project) + "/services/" + esc(service) + "/crons")
}

func (k *KusoClient) GetCron(project, service, name string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + esc(project) + "/services/" + esc(service) + "/crons/" + esc(name))
}

func (k *KusoClient) AddCron(project, service string, req CreateCronRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/projects/" + esc(project) + "/services/" + esc(service) + "/crons")
}

func (k *KusoClient) UpdateCron(project, service, name string, req UpdateCronRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Patch("/api/projects/" + esc(project) + "/services/" + esc(service) + "/crons/" + esc(name))
}

func (k *KusoClient) DeleteCron(project, service, name string) (*resty.Response, error) {
	return k.client.Delete("/api/projects/" + esc(project) + "/services/" + esc(service) + "/crons/" + esc(name))
}

func (k *KusoClient) SyncCron(project, service, name string) (*resty.Response, error) {
	return k.client.Post("/api/projects/" + esc(project) + "/services/" + esc(service) + "/crons/" + esc(name) + "/sync")
}

// AddProjectCron creates a kind=http or kind=command cron at the
// project level (no parent service required).
func (k *KusoClient) AddProjectCron(project string, req CreateProjectCronRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/projects/" + esc(project) + "/crons")
}

// UpdateProjectCron PATCHes a project-scoped cron in place — same
// CR identity, helm-operator does an in-place update so the cronjob
// doesn't briefly disappear.
func (k *KusoClient) UpdateProjectCron(project, name string, req UpdateProjectCronRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Patch("/api/projects/" + esc(project) + "/crons/" + esc(name))
}

// DeleteProjectCron removes a project-scoped cron. Service-attached
// crons go through DeleteCron (different CR-name shape).
func (k *KusoClient) DeleteProjectCron(project, name string) (*resty.Response, error) {
	return k.client.Delete("/api/projects/" + esc(project) + "/crons/" + esc(name))
}
