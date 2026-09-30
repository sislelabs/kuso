// Build pipeline + GitHub installations API client. Mirrors the v0.2
// server endpoints in projects/builds.controller.ts and github/.

package kusoApi

import (
	"fmt"

	"github.com/go-resty/resty/v2"
)

type CreateBuildRequest struct {
	Branch string `json:"branch,omitempty"`
	Ref    string `json:"ref,omitempty"`
	// DryRun runs compile + image-layer assembly but skips registry
	// push and env promotion. Use for "does this PR even build?"
	// without burning storage or rolling prod.
	DryRun bool `json:"dryRun,omitempty"`
	// Env targets an environment by env-group label (staging,
	// preview-pr-7): the server builds that env's branch and bakes that
	// env's build-time vars.
	Env string `json:"env,omitempty"`
}

func (k *KusoClient) ListBuilds(project, service string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + esc(project) + "/services/" + esc(service) + "/builds")
}

// ListBuildsPage is ListBuilds with offset paging. limit <= 0 and
// offset <= 0 are omitted (server returns the full legacy response
// then). When the returned window was cut, the response carries
// X-Kuso-Truncated: true and X-Kuso-Next-Offset: <n> — pass that as
// offset for the next page. The body stays a bare []BuildSummary
// either way.
func (k *KusoClient) ListBuildsPage(project, service string, limit, offset int) (*resty.Response, error) {
	path := "/api/projects/" + esc(project) + "/services/" + esc(service) + "/builds"
	sep := "?"
	if limit > 0 {
		path += fmt.Sprintf("%slimit=%d", sep, limit)
		sep = "&"
	}
	if offset > 0 {
		path += fmt.Sprintf("%soffset=%d", sep, offset)
	}
	return k.client.Get(path)
}

func (k *KusoClient) CreateBuild(project, service string, req CreateBuildRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/projects/" + esc(project) + "/services/" + esc(service) + "/builds")
}

// RollbackBuild re-points the production env at a previous build's
// image. Server validates phase=succeeded.
// env scopes the rollback to a named environment; empty means the
// server's default ("production"). Without this the CLI could only ever
// roll production back — a bad staging build was un-rollbackable, and
// worse, `kuso build rollback` aimed at staging silently rolled
// PRODUCTION back instead. The server has read ?env= since v0.17.1.
// force rolls back even when the build's branch differs from the one the
// env deploys (the server refuses that with 400 otherwise).
func (k *KusoClient) RollbackBuild(project, service, build, env string, force bool) (*resty.Response, error) {
	url := "/api/projects/" + esc(project) + "/services/" + esc(service) + "/builds/" + esc(build) + "/rollback"
	if env != "" {
		url += "?env=" + esc(env)
	}
	k.client.SetBody(map[string]any{"env": env, "force": force})
	return k.client.Post(url)
}

// RetryRelease re-runs the release hook of a release-failed build and
// promotes it on success. 202 {"job": "<release job>"}.
func (k *KusoClient) RetryRelease(project, service, build string) (*resty.Response, error) {
	return k.client.Post("/api/projects/" + esc(project) + "/services/" + esc(service) + "/builds/" + esc(build) + "/retry-release")
}

// RestartService rolls an env's pods on their current image, without a
// build. env empty = production. 202 {"restartedAt": RFC3339}.
func (k *KusoClient) RestartService(project, service, env string) (*resty.Response, error) {
	k.client.SetBody(map[string]string{"env": env})
	return k.client.Post("/api/projects/" + esc(project) + "/services/" + esc(service) + "/restart")
}

// CancelBuild stops an in-flight build. The build CR is preserved
// (with phase=cancelled) so it stays visible in `kuso build list`.
// 409 from the server means the build already reached a terminal
// phase — there's nothing to stop.
func (k *KusoClient) CancelBuild(project, service, build string) (*resty.Response, error) {
	return k.client.Post("/api/projects/" + esc(project) + "/services/" + esc(service) + "/builds/" + esc(build) + "/cancel")
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
