// audit.go — client methods for the read-only audit log surface the
// web audit view drives. Two reads: the instance-wide / per-project
// list (GET /api/audit, narrowed with ?project= + ?limit=), and the
// per-app history (GET /api/audit/app/{pipeline}/{phase}/{app}).
//
// Server-side gating: the cross-project list is admin-only; pass a
// project (or use the per-app route) and project Viewer is enough.

package kusoApi

import (
	"net/url"
	"strconv"

	"github.com/go-resty/resty/v2"
)

// AuditEntry is one audit row. Mirrors server-go internal/audit.Entry.
// Pipeline is the v0.2 project label; App is the service.
type AuditEntry struct {
	ID        int64  `json:"id"`
	Timestamp string `json:"timestamp"`
	User      string `json:"user"`
	Severity  string `json:"severity"`
	Action    string `json:"action"`
	Namespace string `json:"namespace"`
	Phase     string `json:"phase"`
	App       string `json:"app"`
	Pipeline  string `json:"pipeline"`
	Resource  string `json:"resource"`
	Message   string `json:"message"`
}

// AuditResponse is the wire shape both audit reads return: the rows
// plus the total count and the effective limit the server applied.
type AuditResponse struct {
	Audit []AuditEntry `json:"audit"`
	Count int          `json:"count"`
	Limit int          `json:"limit"`
}

// ListAudit returns audit rows. With project set, the call is project-
// scoped (Viewer is enough); empty project asks for the cross-project
// instance-wide view (admin-only). limit <= 0 leaves it to the server
// default (100). Response: AuditResponse.
func (k *KusoClient) ListAudit(project string, limit int) (*resty.Response, error) {
	q := url.Values{}
	if project != "" {
		q.Set("project", project)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	path := "/api/audit"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	return k.client.Get(path)
}

// ListAuditForApp returns the audit history for one service. pipeline
// is the project, app the service; phase is the deploy phase label
// (commonly "production"). Gated on Viewer of the pipeline project.
// Response: AuditResponse.
func (k *KusoClient) ListAuditForApp(pipeline, phase, app string, limit int) (*resty.Response, error) {
	path := "/api/audit/app/" + esc(pipeline) + "/" + esc(phase) + "/" + esc(app)
	if limit > 0 {
		path += "?limit=" + strconv.Itoa(limit)
	}
	return k.client.Get(path)
}
