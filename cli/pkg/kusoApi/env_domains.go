// Env-scoped domain methods. The per-env additionalHosts list controls
// which extra DNS names route to ONE environment (staging, preview-pr-N,
// …) rather than the service as a whole.
//
// AddEnvDomain / RemoveEnvDomain already live in projects.go. This file
// adds the bulk PUT (whole-list replace) that the dashboard Networking
// section uses, so the CLI can offer a `set` verb alongside add/rm.

package kusoApi

import "github.com/go-resty/resty/v2"

// SetEnvDomains replaces an environment's additionalHosts list outright.
// PUT .../services/{s}/envs/{env}/domains with {"hosts": [...]}. A nil
// or empty slice clears every additional host on that env. Returns the
// updated env CR.
func (k *KusoClient) SetEnvDomains(project, service, env string, hosts []string) (*resty.Response, error) {
	if hosts == nil {
		hosts = []string{}
	}
	k.client.SetBody(map[string]any{"hosts": hosts})
	return k.client.Put("/api/projects/" + esc(project) + "/services/" + esc(service) + "/envs/" + esc(env) + "/domains")
}

// IngressTarget is GET /api/config/ingress: where custom-domain DNS
// should point. Source is "loadbalancer", "nodes" or "none".
type IngressTarget struct {
	IPs       []string `json:"ips"`
	Hostnames []string `json:"hostnames"`
	Source    string   `json:"source"`
}

// GetIngressTarget returns the cluster's public ingress address(es).
// Older servers 404; callers treat that as "unknown".
func (k *KusoClient) GetIngressTarget() (*resty.Response, error) {
	return k.client.Get("/api/config/ingress")
}
