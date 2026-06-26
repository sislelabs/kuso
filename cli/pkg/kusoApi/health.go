// health.go — client methods for the /api/health/reconcile platform-
// trust surface that the web /settings/health view and `kuso health`
// drive: a read-only reconcile scan that flags drifted/unsafe resources,
// plus a remediate POST that applies the suggested fix for one issue.
//
// Both endpoints are admin-gated server-side; the command layer just
// surfaces the 403 cleanly. Methods follow the projects.go idiom: take
// args, SetBody(req) before the body-bearing verb, and return the raw
// (*resty.Response, error) so the command layer maps status codes.

package kusoApi

import "github.com/go-resty/resty/v2"

// RemediateRequest is the body for the reconcile remediate POST.
// Action, when empty, tells the server to re-scan and resolve the issue's
// canonical action itself — the caller only has to name the resource.
type RemediateRequest struct {
	Resource string `json:"resource"`
	Action   string `json:"action,omitempty"`
}

// ReconcileHealth runs the platform-trust reconcile scan and returns the
// list of flagged issues plus the healthy/scanned/severity rollup.
// Read-only. Response (admin):
//
//	{issues:[{resource,namespace,project,type,addonKind,kind,severity,
//	  summary,detail,action,safe,fix,runbookCmd}],
//	 healthy, scanned, critical, warning, info}
func (k *KusoClient) ReconcileHealth() (*resty.Response, error) {
	return k.client.Get("/api/health/reconcile")
}

// Remediate applies the suggested fix for a single reconcile issue. When
// action is empty the server re-scans to find the issue's canonical
// action. Mutates live infra. Response:
//
//	{resource, action, applied, message}
func (k *KusoClient) Remediate(resource, action string) (*resty.Response, error) {
	k.client.SetBody(RemediateRequest{Resource: resource, Action: action})
	return k.client.Post("/api/health/reconcile/remediate")
}
