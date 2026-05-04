// Addon backup CLI surface — list S3 backups + restore-into-this OR
// restore-into-sibling. Maps to:
//   GET    /api/projects/{p}/addons/{a}/backups
//   POST   /api/projects/{p}/addons/{a}/backups/restore   {key, into?}

package kusoApi

import "github.com/go-resty/resty/v2"

type RestoreBackupRequest struct {
	Key string `json:"key"`
	// Into = sibling addon name. Empty = in-place (destructive).
	Into string `json:"into,omitempty"`
}

func (k *KusoClient) ListAddonBackups(project, addon string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + project + "/addons/" + addon + "/backups")
}

func (k *KusoClient) RestoreAddonBackup(project, addon string, req RestoreBackupRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/projects/" + project + "/addons/" + addon + "/backups/restore")
}
