// Typed clients for routes the web UI used and the CLI didn't (the
// 2026-10-15 parity audit): env metrics, DB row writes, env-group
// branch pins, incident-agent settings, node remove preflight, admin
// token management, drain get/update, orphan delete, notification
// event types, cluster storage classes / ingress hosts, GitHub setup
// status + repo tree, and local invite redemption.

package kusoApi

import (
	"net/url"
	"strconv"

	"github.com/go-resty/resty/v2"
)

// ---------- service metrics ----------

// EnvMetrics returns per-pod CPU + memory for one env CR:
// {"env","window","pods":[{"pod","timestamp","cpuMillicores","memBytes"}]}.
func (k *KusoClient) EnvMetrics(envCR string) (*resty.Response, error) {
	return k.client.Get("/api/kubernetes/envs/" + esc(envCR) + "/metrics")
}

// EnvTimeseries returns requests/errors/p95ms series for one env CR.
// Empty rangeStr/step use the server defaults (1h, auto step).
func (k *KusoClient) EnvTimeseries(envCR, rangeStr, step string) (*resty.Response, error) {
	q := url.Values{}
	if rangeStr != "" {
		q.Set("range", rangeStr)
	}
	if step != "" {
		q.Set("step", step)
	}
	path := "/api/kubernetes/envs/" + esc(envCR) + "/timeseries"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	return k.client.Get(path)
}

// ---------- DB browser row writes ----------

// SQLCell is one cell in a row write. Matches cellValue in
// server-go/internal/http/handlers/sql_data.go.
type SQLCell struct {
	Value  any  `json:"value"`
	IsNull bool `json:"isNull"`
}

// SQLRowWrite is the body of POST/PATCH/DELETE .../sql/rows. Values is
// the insert payload, Set the update payload, PK the full primary key
// for update/delete.
type SQLRowWrite struct {
	Schema string             `json:"schema"`
	Table  string             `json:"table"`
	Values map[string]SQLCell `json:"values,omitempty"`
	Set    map[string]SQLCell `json:"set,omitempty"`
	PK     map[string]SQLCell `json:"pk,omitempty"`
}

func sqlRowsPath(project, addon, database string) string {
	return "/api/projects/" + esc(project) + "/addons/" + esc(addon) + "/sql/rows" + dbQS(database)
}

// SQLInsertRow → {"columns","row"}.
func (k *KusoClient) SQLInsertRow(project, addon, database string, body SQLRowWrite) (*resty.Response, error) {
	k.client.SetBody(body)
	return k.client.Post(sqlRowsPath(project, addon, database))
}

// SQLUpdateRow → {"columns","row"}; 404 when the PK matched nothing.
func (k *KusoClient) SQLUpdateRow(project, addon, database string, body SQLRowWrite) (*resty.Response, error) {
	k.client.SetBody(body)
	return k.client.Patch(sqlRowsPath(project, addon, database))
}

// SQLDeleteRow → {"deleted":1}; 404 when the PK matched nothing.
func (k *KusoClient) SQLDeleteRow(project, addon, database string, body SQLRowWrite) (*resty.Response, error) {
	k.client.SetBody(body)
	return k.client.Delete(sqlRowsPath(project, addon, database))
}

// ---------- env-groups ----------

// SetEnvGroupServiceBranch points one service in a non-production
// env-group at a branch. 204 on success.
func (k *KusoClient) SetEnvGroupServiceBranch(project, group, service, branch string) (*resty.Response, error) {
	k.client.SetBody(map[string]string{"branch": branch})
	return k.client.Patch("/api/projects/" + esc(project) + "/env-groups/" + esc(group) + "/services/" + esc(service) + "/branch")
}

// ---------- incidents ----------

// IncidentAgentConfig mirrors server-go db.IncidentAgentConfig. PUT is a
// full replace, so callers read-modify-write.
type IncidentAgentConfig struct {
	Enabled       bool   `json:"enabled"`
	TriggerPod    bool   `json:"triggerPod"`
	TriggerAlert  bool   `json:"triggerAlert"`
	TriggerNode   bool   `json:"triggerNode"`
	MaxConcurrent int    `json:"maxConcurrent"`
	CooldownHours int    `json:"cooldownHours"`
	AgentImage    string `json:"agentImage,omitempty"`
}

// IncidentAgentDiscord is the PUT .../incident-agent/discord body. Empty
// tokens keep the stored value; ChannelID nil keeps, "" clears.
type IncidentAgentDiscord struct {
	BotToken     string  `json:"botToken,omitempty"`
	KusoBotToken string  `json:"kusoBotToken,omitempty"`
	ChannelID    *string `json:"channelId,omitempty"`
}

// GetIncidentAgentSettings → {"config": IncidentAgentConfig, "status": {...}}.
func (k *KusoClient) GetIncidentAgentSettings() (*resty.Response, error) {
	return k.client.Get("/api/admin/settings/incident-agent")
}

func (k *KusoClient) PutIncidentAgentSettings(cfg IncidentAgentConfig) (*resty.Response, error) {
	k.client.SetBody(cfg)
	return k.client.Put("/api/admin/settings/incident-agent")
}

func (k *KusoClient) PutIncidentAgentDiscord(body IncidentAgentDiscord) (*resty.Response, error) {
	k.client.SetBody(body)
	return k.client.Put("/api/admin/settings/incident-agent/discord")
}

// IncidentFeedbackText posts a free-text operator reply. The server's
// decision field is left empty so this can never approve or reject.
func (k *KusoClient) IncidentFeedbackText(id, text string) (*resty.Response, error) {
	k.client.SetBody(map[string]string{"text": text})
	return k.client.Post("/api/incidents/" + esc(id) + "/feedback")
}

// ---------- nodes ----------

// NodeRemovePreflight → {"pinned":[{"namespace","pvc","kind","project","owner","size"}]}.
func (k *KusoClient) NodeRemovePreflight(name string) (*resty.Response, error) {
	return k.client.Get("/api/kubernetes/nodes/" + esc(name) + "/remove-preflight")
}

// ---------- users ----------

// UpdateUserRequest is the partial PUT /api/users/id/{id} body; nil
// fields are left untouched server-side.
type UpdateUserRequest struct {
	FirstName *string `json:"firstName,omitempty"`
	LastName  *string `json:"lastName,omitempty"`
	Email     *string `json:"email,omitempty"`
	RoleID    *string `json:"roleId,omitempty"`
	IsActive  *bool   `json:"isActive,omitempty"`
}

func (k *KusoClient) UpdateUser(id string, req UpdateUserRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Put("/api/users/id/" + esc(id))
}

// ChangeMyPassword → 204; 403 when current is wrong. The server
// invalidates every token the caller holds on success.
func (k *KusoClient) ChangeMyPassword(current, next string) (*resty.Response, error) {
	k.client.SetBody(map[string]string{"currentPassword": current, "newPassword": next})
	return k.client.Put("/api/users/profile/password")
}

// ---------- tokens (admin) ----------

// ListAllTokens → [{id,name,createdAt,expiresAt,isActive,user:{id,username,email}}].
func (k *KusoClient) ListAllTokens() (*resty.Response, error) {
	return k.client.Get("/api/tokens")
}

// CreateTokenForUser → {name, token, expiresAt}.
func (k *KusoClient) CreateTokenForUser(userID string, req CreateTokenRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/tokens/user/" + esc(userID))
}

// DeleteAnyToken revokes a token regardless of owner (user:write).
func (k *KusoClient) DeleteAnyToken(id string) (*resty.Response, error) {
	return k.client.Delete("/api/tokens/" + esc(id))
}

// ---------- drains ----------

func (k *KusoClient) GetDrain(id string) (*resty.Response, error) {
	return k.client.Get("/api/drains/" + esc(id))
}

// UpdateDrain is a full replace. Header values / secret equal to the
// server's mask sentinel mean "keep the stored value", so a masked GET
// can be written straight back.
func (k *KusoClient) UpdateDrain(id string, body DrainBody) (*resty.Response, error) {
	k.client.SetBody(body)
	return k.client.Put("/api/drains/" + esc(id))
}

// ---------- reconcile health orphans ----------

// DeleteOrphan removes one orphaned addon conn Secret or PVC. The server
// re-verifies the owning addon is gone (409 otherwise).
func (k *KusoClient) DeleteOrphan(kind, namespace, name string) (*resty.Response, error) {
	q := url.Values{}
	q.Set("kind", kind)
	q.Set("namespace", namespace)
	q.Set("name", name)
	return k.client.Delete("/api/admin/orphans?" + q.Encode())
}

// ---------- notifications ----------

// NotificationEventTypes → [{type,label,group,defaultMention}].
func (k *KusoClient) NotificationEventTypes() (*resty.Response, error) {
	return k.client.Get("/api/notifications/event-types")
}

// ---------- cluster ----------

// StorageClasses → []storagev1.StorageClass (admin).
func (k *KusoClient) StorageClasses() (*resty.Response, error) {
	return k.client.Get("/api/kubernetes/storageclasses")
}

// IngressDomains → sorted []string of every Ingress host (admin).
func (k *KusoClient) IngressDomains() (*resty.Response, error) {
	return k.client.Get("/api/kubernetes/domains")
}

// ---------- github ----------

// GithubSetupStatus → {configured, appSlug?, appId?} (admin).
func (k *KusoClient) GithubSetupStatus() (*resty.Response, error) {
	return k.client.Get("/api/github/setup-status")
}

// GithubRepoTree → [{path,type,size}] at HEAD of branch, optionally
// under pathPrefix (stripped from returned paths).
func (k *KusoClient) GithubRepoTree(installationID int64, owner, repo, branch, pathPrefix string) (*resty.Response, error) {
	q := url.Values{}
	q.Set("branch", branch)
	if pathPrefix != "" {
		q.Set("path", pathPrefix)
	}
	return k.client.Get("/api/github/installations/" + strconv.FormatInt(installationID, 10) +
		"/repos/" + esc(owner) + "/" + esc(repo) + "/tree?" + q.Encode())
}

// ---------- invites ----------

// RedeemInviteLocal creates a local account from an invite token →
// {"access_token": "<jwt>"}. Unauthenticated route.
func (k *KusoClient) RedeemInviteLocal(token, username, email, password string) (*resty.Response, error) {
	k.client.SetBody(map[string]string{"token": token, "username": username, "email": email, "password": password})
	return k.client.Post("/api/invites/redeem")
}
