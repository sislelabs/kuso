// github.go — client methods for the /api/github/* admin surface. Today
// this is just the webhook-health round-trip probe that `kuso doctor`
// uses to confirm the GitHub App is configured and that pushes are
// actually reaching kuso.
//
// Follows the health.go idiom: one-line method that returns the raw
// (*resty.Response, error) so the command layer decides how to decode
// and maps the status code (webhook-health is admin-gated server-side).

package kusoApi

import "github.com/go-resty/resty/v2"

// WebhookHealth runs the GitHub webhook-health probe: is the App's
// webhook secret configured, and when did the last verified delivery
// land. Read-only. Response (admin):
//
//	{configured: bool, lastDeliveryAt?: string, lastDeliveryEvent?: string}
func (k *KusoClient) WebhookHealth() (*resty.Response, error) {
	if err := k.ensureReady(); err != nil {
		return nil, err
	}
	return k.client.Get("/api/github/webhook-health")
}
