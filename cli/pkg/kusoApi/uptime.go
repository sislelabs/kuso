package kusoApi

import "github.com/go-resty/resty/v2"

// UptimePatch is the `uptime` block of a project or service PATCH.
// Nil fields are left alone; Path is service-only and "" clears it.
type UptimePatch struct {
	Disabled *bool   `json:"disabled,omitempty"`
	Path     *string `json:"path,omitempty"`
}

// PatchProjectUptimeBody is a PATCH /api/projects/{project} body that
// touches only spec.uptime. CLI-local because apiv1.UpdateProjectRequest
// has no uptime field yet.
type PatchProjectUptimeBody struct {
	Uptime UptimePatch `json:"uptime"`
}

type UptimeServiceStatus struct {
	Service       string `json:"service"`
	Env           string `json:"env"`
	State         string `json:"state"`
	Reason        string `json:"reason,omitempty"`
	Since         string `json:"since,omitempty"`
	LastCheckedAt string `json:"lastCheckedAt,omitempty"`
	LatencyMs     int64  `json:"latencyMs,omitempty"`
	StatusCode    int    `json:"statusCode,omitempty"`
	Error         string `json:"error,omitempty"`
	URL           string `json:"url"`
}

// UptimeStatus is the GET /api/projects/{project}/uptime response.
// Enabled=false means the instance-wide kill switch is set.
type UptimeStatus struct {
	Enabled  bool                  `json:"enabled"`
	Services []UptimeServiceStatus `json:"services"`
}

func (k *KusoClient) GetProjectUptime(project string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + esc(project) + "/uptime")
}

func (k *KusoClient) PatchProjectUptime(project string, patch UptimePatch) (*resty.Response, error) {
	k.client.SetBody(PatchProjectUptimeBody{Uptime: patch})
	return k.client.Patch("/api/projects/" + esc(project))
}
