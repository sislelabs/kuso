// Marketplace API client. Mirrors /api/marketplace.
package kusoApi

import "github.com/go-resty/resty/v2"

// MarketplaceList GETs the app catalog.
func (k *KusoClient) MarketplaceList() (*resty.Response, error) {
	return k.client.Get("/api/marketplace")
}

// MarketplaceGet GETs one app's manifest (metadata + prompts).
func (k *KusoClient) MarketplaceGet(app string) (*resty.Response, error) {
	return k.client.Get("/api/marketplace/" + esc(app))
}

// MarketplaceRender POSTs answers and returns the rendered kuso.yaml.
// Matches the AddProjectCron idiom: SetBody on the shared client, then
// Post. resty marshals the struct to JSON.
func (k *KusoClient) MarketplaceRender(app, project string, answers map[string]string) (*resty.Response, error) {
	k.client.SetBody(map[string]any{"project": project, "answers": answers})
	return k.client.Post("/api/marketplace/" + esc(app) + "/render")
}
