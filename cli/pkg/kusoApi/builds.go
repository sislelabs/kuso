// Build pipeline + GitHub installations API client. Mirrors the v0.2
// server endpoints in projects/builds.controller.ts and github/.

package kusoApi

import "github.com/go-resty/resty/v2"

type CreateBuildRequest struct {
	Branch string `json:"branch,omitempty"`
	Ref    string `json:"ref,omitempty"`
}

func (k *KusoClient) ListBuilds(project, service string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + project + "/services/" + service + "/builds")
}

func (k *KusoClient) CreateBuild(project, service string, req CreateBuildRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/projects/" + project + "/services/" + service + "/builds")
}

// RollbackBuild re-points the production env at a previous build's
// image. Server validates phase=succeeded.
func (k *KusoClient) RollbackBuild(project, service, build string) (*resty.Response, error) {
	return k.client.Post("/api/projects/" + project + "/services/" + service + "/builds/" + build + "/rollback")
}

// ---------- GitHub installations ----------

func (k *KusoClient) GetInstallURL() (*resty.Response, error) {
	return k.client.Get("/api/github/install-url")
}

func (k *KusoClient) ListInstallations() (*resty.Response, error) {
	return k.client.Get("/api/github/installations")
}

func (k *KusoClient) ListInstallationRepos(id int64) (*resty.Response, error) {
	return k.client.Get("/api/github/installations/" + itoa(id) + "/repos")
}

func (k *KusoClient) RefreshInstallations() (*resty.Response, error) {
	return k.client.Post("/api/github/installations/refresh")
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	if neg {
		digits = "-" + digits
	}
	return digits
}
