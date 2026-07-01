// Service stop/start API methods. A hard-stop pins the service to 0
// replicas and disables wake-on-traffic (visitors get a 503); start
// clears the stop. Kept in a separate file from projects.go so the
// service surface can grow without churning the core project client.

package kusoApi

import "github.com/go-resty/resty/v2"

// StopService hard-stops a service: pins it to 0 replicas and disables
// wake-on-traffic. POST .../services/{s}/stop, no body. Server returns
// 202 Accepted — the scale-down is asynchronous.
func (k *KusoClient) StopService(project, service string) (*resty.Response, error) {
	return k.client.Post("/api/projects/" + esc(project) + "/services/" + esc(service) + "/stop")
}

// StartService clears a hard-stop, restoring the service to its
// configured replicas. POST .../services/{s}/start, no body. Server
// returns 202 Accepted.
func (k *KusoClient) StartService(project, service string) (*resty.Response, error) {
	return k.client.Post("/api/projects/" + esc(project) + "/services/" + esc(service) + "/start")
}
