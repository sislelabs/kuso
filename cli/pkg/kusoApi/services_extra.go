// Service-level API methods that round out CLI parity with the web UI:
// waking a slept service and reading its drift report. Kept in a
// separate file from projects.go so the service surface can grow
// without churning the core project client.

package kusoApi

import "github.com/go-resty/resty/v2"

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
