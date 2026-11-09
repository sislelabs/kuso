// Service-level API methods that round out CLI parity with the web UI:
// waking a slept service and reading its drift report. Kept in a
// separate file from projects.go so the service surface can grow
// without churning the core project client.

package kusoApi

import (
	"encoding/json"

	"github.com/go-resty/resty/v2"
)

// WakeService kicks a slept (scale-to-zero) service back to its
// configured minimum replicas. POST .../services/{s}/wake, no body.
// Server returns 202 Accepted — the wake is asynchronous; the pod
// comes up on the next reconcile.
func (k *KusoClient) WakeService(project, service string) (*resty.Response, error) {
	return k.client.Post("/api/projects/" + esc(project) + "/services/" + esc(service) + "/wake")
}

// GetServiceDrift returns the drift report for a service: whether the
// saved spec is reflected on the production env CR, whether the
// helm-operator has observed the latest generation, and whether the
// running pods match the rendered template. GET .../services/{s}/drift.
func (k *KusoClient) GetServiceDrift(project, service string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + esc(project) + "/services/" + esc(service) + "/drift")
}

// ServiceChangeEnvSet is one variable write in a batch; the server picks
// the storage exactly as the per-key PUT with auto:true does.
type ServiceChangeEnvSet struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ServiceChangesRequest mirrors the server's projects.ServiceChangesRequest:
// variable writes, removals and one spec patch, applied together so each
// env rolls at most once. Patch is a PATCH .../services/{s} body.
type ServiceChangesRequest struct {
	EnvSet   []ServiceChangeEnvSet `json:"envSet,omitempty"`
	EnvUnset []string              `json:"envUnset,omitempty"`
	Patch    json.RawMessage       `json:"patch,omitempty"`
	Message  string                `json:"message,omitempty"`
}

// ServiceChangesResult is the 200 body: per-item outcomes (failed items
// are left unapplied) and whether the single rollout write happened.
type ServiceChangesResult struct {
	Results []struct {
		Kind  string `json:"kind"`
		Name  string `json:"name,omitempty"`
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	} `json:"results"`
	RolledOut  bool   `json:"rolledOut"`
	FlushError string `json:"flushError,omitempty"`
}

// ApplyServiceChanges is POST .../services/{s}/changes.
func (k *KusoClient) ApplyServiceChanges(project, service string, req ServiceChangesRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/projects/" + esc(project) + "/services/" + esc(service) + "/changes")
}
