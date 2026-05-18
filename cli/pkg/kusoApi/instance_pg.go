// Cluster-shared Postgres — the first-class managed-PG story. Wraps
// /api/instance-pg/* on the server. See server-go/internal/instancepg
// for the underlying state machine; this is just the wire client.
//
// Three operations on the same admin DSN:
//   * status      — read which mode is in play + the consumer count
//   * managed     — provision the on-cluster Postgres via the helm
//                   chart; returns immediately, completion is async
//   * external    — register an off-cluster DSN; server runs a real
//                   SELECT 1 before storing (validation is sync)
//   * disable     — tear down; refused while consumers still depend

package kusoApi

import "github.com/go-resty/resty/v2"

// ProvisionInstancePGRequest is the body for POST
// /api/instance-pg/managed. All fields optional — defaults
// produce a sensible small-dev Postgres.
type ProvisionInstancePGRequest struct {
	Size        string `json:"size,omitempty"`
	HA          bool   `json:"ha,omitempty"`
	Version     string `json:"version,omitempty"`
	StorageSize string `json:"storageSize,omitempty"`
}

// ConfigureExternalInstancePGRequest is the body for POST
// /api/instance-pg/external. The DSN is validated server-side
// (open + ping + SELECT 1) before being persisted.
type ConfigureExternalInstancePGRequest struct {
	DSN string `json:"dsn"`
}

func (k *KusoClient) GetInstancePG() (*resty.Response, error) {
	return k.client.Get("/api/instance-pg")
}

func (k *KusoClient) ProvisionInstancePG(req ProvisionInstancePGRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/instance-pg/managed")
}

func (k *KusoClient) ConfigureExternalInstancePG(req ConfigureExternalInstancePGRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/instance-pg/external")
}

func (k *KusoClient) DisableInstancePG() (*resty.Response, error) {
	return k.client.Delete("/api/instance-pg")
}
