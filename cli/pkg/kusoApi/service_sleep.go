package kusoApi

import "github.com/go-resty/resty/v2"

// PatchSleepBody is the PATCH /api/projects/{p}/services/{s} body that
// touches only spec.sleep. Kept apart from PatchServiceRequest so the
// sleep command doesn't depend on that CLI-local subset.
type PatchSleepBody struct {
	Sleep PatchSleepRequest `json:"sleep"`
}

// PatchSleepRequest mirrors the server's projects.PatchSleepRequest.
// Enabled governs the production env; NonProduction ("on"|"off") governs
// every other env, which sleeps by default.
type PatchSleepRequest struct {
	Enabled       *bool  `json:"enabled,omitempty"`
	AfterMinutes  *int   `json:"afterMinutes,omitempty"`
	NonProduction string `json:"nonProduction,omitempty"`
}

// PatchServiceSleep applies a sleep-only partial update.
func (k *KusoClient) PatchServiceSleep(project, service string, body PatchSleepBody) (*resty.Response, error) {
	k.client.SetBody(body)
	return k.client.Patch("/api/projects/" + esc(project) + "/services/" + esc(service))
}
