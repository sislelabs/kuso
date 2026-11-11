package kusoApi

import "github.com/go-resty/resty/v2"

// EnvIsolationReport is GET /api/projects/{project}/env-isolation.
type EnvIsolationReport struct {
	Project           string   `json:"project"`
	Enabled           bool     `json:"enabled"`
	NonProductionEnvs bool     `json:"nonProductionEnvs"`
	Ready             bool     `json:"ready"`
	UnstampedEnvs     []string `json:"unstampedEnvs"`
	UnlabelledEnvs    []string `json:"unlabelledEnvs"`
	UnlabelledPods    []string `json:"unlabelledPods"`
}

type SetEnvIsolationRequest struct {
	Enabled           bool  `json:"enabled"`
	NonProductionEnvs *bool `json:"nonProductionEnvs,omitempty"`
}

func (k *KusoClient) GetEnvIsolation(project string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + esc(project) + "/env-isolation")
}

// StampEnvIsolation writes spec.envScope on every env CR lacking it, which
// rolls those envs' pods once.
func (k *KusoClient) StampEnvIsolation(project string) (*resty.Response, error) {
	return k.client.Post("/api/projects/" + esc(project) + "/env-isolation/stamp")
}

func (k *KusoClient) SetEnvIsolation(project string, req SetEnvIsolationRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Put("/api/projects/" + esc(project) + "/env-isolation")
}
