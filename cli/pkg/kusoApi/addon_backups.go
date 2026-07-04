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
	// Confirm must echo the destination addon name for an in-place
	// restore (server rejects otherwise). Ignored for --into siblings.
	Confirm string `json:"confirm,omitempty"`
}

func (k *KusoClient) ListAddonBackups(project, addon string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + esc(project) + "/addons/" + esc(addon) + "/backups")
}

func (k *KusoClient) RestoreAddonBackup(project, addon string, req RestoreBackupRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/projects/" + esc(project) + "/addons/" + esc(addon) + "/backups/restore")
}

// DownloadAddonBackup streams an on-demand dump of the addon (postgres →
// gzipped SQL, s3 → gzipped tar). Works even when scheduled S3 backups
// aren't configured. Response body carries the bytes; the caller reads
// the Content-Disposition filename off the response header.
func (k *KusoClient) DownloadAddonBackup(project, addon string) (*resty.Response, error) {
	return k.RawGet("/api/projects/" + esc(project) + "/addons/" + esc(addon) + "/backups/download")
}
