// S3 addon credential rotation and backup-orphan admin. Maps to:
//   POST /api/projects/{p}/addons/{a}/s3/rotate-credentials
//   POST /api/projects/{p}/addons/{a}/s3/revoke-previous
//   GET  /api/admin/backups/orphans
//   POST /api/admin/backups/orphans/purge   {project, addon, confirm}

package kusoApi

import "github.com/go-resty/resty/v2"

type RotateS3CredentialsRequest struct {
	RotateRoot bool `json:"rotateRoot,omitempty"`
}

func (k *KusoClient) RotateS3Credentials(project, addon string, req RotateS3CredentialsRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/projects/" + esc(project) + "/addons/" + esc(addon) + "/s3/rotate-credentials")
}

func (k *KusoClient) RevokePreviousS3Credentials(project, addon string) (*resty.Response, error) {
	return k.client.Post("/api/projects/" + esc(project) + "/addons/" + esc(addon) + "/s3/revoke-previous")
}

func (k *KusoClient) ListOrphanBackups() (*resty.Response, error) {
	return k.client.Get("/api/admin/backups/orphans")
}

type PurgeOrphanBackupsRequest struct {
	Project string `json:"project"`
	Addon   string `json:"addon"`
	Confirm string `json:"confirm"`
}

func (k *KusoClient) PurgeOrphanBackups(req PurgeOrphanBackupsRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/admin/backups/orphans/purge")
}
