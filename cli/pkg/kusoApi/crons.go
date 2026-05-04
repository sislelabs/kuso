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

type UpdateCronRequest struct {
	Schedule              *string  `json:"schedule,omitempty"`
	Command               []string `json:"command,omitempty"`
	Suspend               *bool    `json:"suspend,omitempty"`
	ConcurrencyPolicy     *string  `json:"concurrencyPolicy,omitempty"`
	ActiveDeadlineSeconds *int     `json:"activeDeadlineSeconds,omitempty"`
}

func (k *KusoClient) ListCrons(project string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + project + "/crons")
}

func (k *KusoClient) ListCronsForService(project, service string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + project + "/services/" + service + "/crons")
}

func (k *KusoClient) GetCron(project, service, name string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + project + "/services/" + service + "/crons/" + name)
}

func (k *KusoClient) AddCron(project, service string, req CreateCronRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/projects/" + project + "/services/" + service + "/crons")
}

func (k *KusoClient) UpdateCron(project, service, name string, req UpdateCronRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Patch("/api/projects/" + project + "/services/" + service + "/crons/" + name)
}

func (k *KusoClient) DeleteCron(project, service, name string) (*resty.Response, error) {
	return k.client.Delete("/api/projects/" + project + "/services/" + service + "/crons/" + name)
}

func (k *KusoClient) SyncCron(project, service, name string) (*resty.Response, error) {
	return k.client.Post("/api/projects/" + project + "/services/" + service + "/crons/" + name + "/sync")
}
