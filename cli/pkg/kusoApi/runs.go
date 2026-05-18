// KusoRun API client — one-shot task pods bound to a service's
// most-recent built image. Mirrors server-go/internal/runs/runs.go.

package kusoApi

import "github.com/go-resty/resty/v2"

// CreateRunRequest is the body of POST /api/projects/{p}/services/{s}/runs.
type CreateRunRequest struct {
	Command        []string    `json:"command"`
	Env            []RunEnvVar `json:"env,omitempty"`
	TimeoutSeconds int         `json:"timeoutSeconds,omitempty"`
}

// RunEnvVar matches the server's runs.EnvVar.
type RunEnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func (k *KusoClient) ListRuns(project, service string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + esc(project) + "/services/" + esc(service) + "/runs")
}

func (k *KusoClient) CreateRun(project, service string, req CreateRunRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/projects/" + esc(project) + "/services/" + esc(service) + "/runs")
}

func (k *KusoClient) GetRun(project, run string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + esc(project) + "/runs/" + esc(run))
}

func (k *KusoClient) CancelRun(project, run string) (*resty.Response, error) {
	return k.client.Post("/api/projects/" + esc(project) + "/runs/" + esc(run) + "/cancel")
}

func (k *KusoClient) DeleteRun(project, run string) (*resty.Response, error) {
	return k.client.Delete("/api/projects/" + esc(project) + "/runs/" + esc(run))
}
