// Instance-config API client. Mirrors GET/POST /api/config — the Kuso
// CR spec ("settings"). GET is readable by any authenticated user; POST
// requires settings:admin (AdminOnly middleware) and replaces the whole
// settings object.
package kusoApi

import "github.com/go-resty/resty/v2"

// GetInstanceConfig reads the cached Kuso CR spec. GET /api/config. The
// body is {settings:{...}, secrets:{...}}.
func (k *KusoClient) GetInstanceConfig() (*resty.Response, error) {
	return k.client.Get("/api/config")
}

// SetInstanceConfig replaces the Kuso CR spec. POST /api/config. The
// body is {settings:{...}} — the entire settings object is replaced, so
// callers should read-modify-write to preserve untouched keys.
func (k *KusoClient) SetInstanceConfig(settings map[string]any) (*resty.Response, error) {
	k.client.SetBody(map[string]any{"settings": settings})
	return k.client.Post("/api/config")
}

// --- config sub-resources ---
//
// PodSize keys are capitalized because the server decodes/encodes the raw
// db.PodSize struct (no json tags): ID, Name, CPULimit, MemoryLimit,
// CPURequest, MemoryRequest, Description.

// PodSize is one named CPU/memory preset offered to services. The server
// (de)serializes the raw db.PodSize struct with no json tags, so keys are
// capitalized and Description is a sql.NullString ({String, Valid}) rather
// than a bare string — hence the nested type here.
type PodSize struct {
	ID            string     `json:"ID,omitempty"`
	Name          string     `json:"Name"`
	CPULimit      string     `json:"CPULimit"`
	MemoryLimit   string     `json:"MemoryLimit"`
	CPURequest    string     `json:"CPURequest"`
	MemoryRequest string     `json:"MemoryRequest"`
	Description   NullString `json:"Description"`
}

// NullString mirrors sql.NullString's JSON shape for round-tripping the
// PodSize.Description field through the untagged server struct.
type NullString struct {
	String string `json:"String"`
	Valid  bool   `json:"Valid"`
}

// NewNullString builds a NullString; Valid is true iff s is non-empty.
func NewNullString(s string) NullString {
	return NullString{String: s, Valid: s != ""}
}

// ListPodSizes returns every PodSize preset. GET /api/config/podsizes.
func (k *KusoClient) ListPodSizes() (*resty.Response, error) {
	return k.client.Get("/api/config/podsizes")
}

// CreatePodSize inserts a PodSize. POST /api/config/podsizes. Admin-gated.
func (k *KusoClient) CreatePodSize(p PodSize) (*resty.Response, error) {
	k.client.SetBody(p)
	return k.client.Post("/api/config/podsizes")
}

// UpdatePodSize replaces a PodSize by id. PUT /api/config/podsizes/{id}.
func (k *KusoClient) UpdatePodSize(id string, p PodSize) (*resty.Response, error) {
	k.client.SetBody(p)
	return k.client.Put("/api/config/podsizes/" + esc(id))
}

// DeletePodSize removes a PodSize by id. DELETE /api/config/podsizes/{id}.
func (k *KusoClient) DeletePodSize(id string) (*resty.Response, error) {
	return k.client.Delete("/api/config/podsizes/" + esc(id))
}

// ListRunpacks returns every runpack (with phases inlined). Read-only —
// there is no create/update route; delete is the only mutation.
func (k *KusoClient) ListRunpacks() (*resty.Response, error) {
	return k.client.Get("/api/config/runpacks")
}

// DeleteRunpack removes a runpack by id. DELETE /api/config/runpacks/{id}.
func (k *KusoClient) DeleteRunpack(id string) (*resty.Response, error) {
	return k.client.Delete("/api/config/runpacks/" + esc(id))
}

// GetConfigTemplates returns the service templates. GET /api/config/templates.
func (k *KusoClient) GetConfigTemplates() (*resty.Response, error) {
	return k.client.Get("/api/config/templates")
}

// GetConfigBanner returns the instance banner. GET /api/config/banner.
func (k *KusoClient) GetConfigBanner() (*resty.Response, error) {
	return k.client.Get("/api/config/banner")
}

// GetConfigClusterIssuer returns the cert-manager cluster issuer.
// GET /api/config/clusterissuer.
func (k *KusoClient) GetConfigClusterIssuer() (*resty.Response, error) {
	return k.client.Get("/api/config/clusterissuer")
}

// GetConfigRegistry returns the image-registry config. GET /api/config/registry.
func (k *KusoClient) GetConfigRegistry() (*resty.Response, error) {
	return k.client.Get("/api/config/registry")
}
