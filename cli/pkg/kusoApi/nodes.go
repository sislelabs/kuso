// nodes.go — client methods for the /api/kubernetes/nodes/* surface
// that the web /settings/nodes view drives: list nodes, edit kuso
// labels, validate/join/remove, host-package updates, history, and
// the cluster cleanup sweep. The bootstrap-token mint/pending/revoke
// flow lives separately in node_bootstrap.go; this file covers the
// rest of the node-management parity gap.
//
// Every method follows the projects.go idiom: take args, build a path
// with esc(), SetBody(req) before a body-bearing verb, return the raw
// (*resty.Response, error) so the command layer maps status codes.

package kusoApi

import "github.com/go-resty/resty/v2"

// NodeSummary mirrors server-go internal/nodeshape.Summary — the slim
// per-node wire shape the UI consumes. Only kuso-managed labels are
// surfaced (KusoLabels, prefix already stripped); the topology
// region/zone are display-only. Values are milli (CPU) or bytes
// (memory/disk).
type NodeSummary struct {
	Name        string            `json:"name"`
	Ready       bool              `json:"ready"`
	Roles       []string          `json:"roles"`
	Region      string            `json:"region,omitempty"`
	Zone        string            `json:"zone,omitempty"`
	KusoLabels  map[string]string `json:"kusoLabels"`
	Schedulable bool              `json:"schedulable"`
	Unreachable bool              `json:"unreachable,omitempty"`
	CreatedAt   string            `json:"createdAt,omitempty"`

	CPUCapacityMilli   int64 `json:"cpuCapacityMilli"`
	CPUUsageMilli      int64 `json:"cpuUsageMilli"`
	MemCapacityBytes   int64 `json:"memCapacityBytes"`
	MemUsageBytes      int64 `json:"memUsageBytes"`
	DiskCapacityBytes  int64 `json:"diskCapacityBytes"`
	DiskAvailableBytes int64 `json:"diskAvailableBytes"`
	Pods               int   `json:"pods"`
	PodsCapacity       int   `json:"podsCapacity"`
}

// NodeCredentials is the SSH credential block validate/join/remove
// share. Mirrors server-go internal/nodejoin.Credentials. Exactly one
// of Password / PrivateKey should be set; Port defaults to 22 server-
// side when zero.
type NodeCredentials struct {
	Host       string `json:"host"`
	Port       int    `json:"port,omitempty"`
	User       string `json:"user"`
	Password   string `json:"password,omitempty"`
	PrivateKey string `json:"privateKey,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
}

// ValidateNodeRequest is the body for the pre-flight check. SSHKeyID,
// when set, tells the server to pull the private key from its SSH key
// library rather than expecting it inline.
type ValidateNodeRequest struct {
	NodeCredentials
	SSHKeyID string `json:"sshKeyId,omitempty"`
}

// ValidateCheck is one pre-flight probe result. Mirrors
// nodejoin.ValidateCheck.
type ValidateCheck struct {
	Label  string `json:"label"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// ValidateNodeResult is the validate response. OK is true only when
// every check passed.
type ValidateNodeResult struct {
	Checks []ValidateCheck `json:"checks"`
	OK     bool            `json:"ok"`
}

// JoinNodeRequest is the body for an SSH-driven join. Labels are baked
// into the k3s-agent install so the node lands in the right region/tier
// from boot; Name overrides the node's hostname.
type JoinNodeRequest struct {
	NodeCredentials
	SSHKeyID string            `json:"sshKeyId,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`
	Name     string            `json:"name,omitempty"`
}

// RemoveNodeRequest is the (optional) body for remove. When Credentials
// is set the server also SSHes in and runs k3s-agent-uninstall to clean
// up the host; without it the node is only untracked from the control
// plane. Force skips graceful pod eviction.
type RemoveNodeRequest struct {
	Credentials *NodeCredentials `json:"credentials,omitempty"`
	Force       bool             `json:"force,omitempty"`
}

// SetNodeLabelsRequest is the body for the label PUT. Bare keys (no
// kuso.sislelabs.com/ prefix) — the server applies the prefix on the
// way in. The PUT is a full replace: any kuso-namespaced label absent
// from this map is deleted.
type SetNodeLabelsRequest struct {
	Labels map[string]string `json:"labels"`
}

// ApplyNodeUpdatesRequest is the body for the host package-update apply.
// AllowReboot=true lets the server run the cordon/drain/reboot
// orchestration when a restart is required.
type ApplyNodeUpdatesRequest struct {
	AllowReboot bool `json:"allowReboot"`
}

// ListNodes returns every cluster node with status + live usage. Admin-
// gated server-side. Response: a bare JSON array of NodeSummary.
func (k *KusoClient) ListNodes() (*resty.Response, error) {
	return k.client.Get("/api/kubernetes/nodes")
}

// NodeHistory returns up-to-7-days of resource samples for sparklines.
// Response: {"node": "<name>", "samples": [NodeMetric, ...]}. Empty
// samples is valid (sampler hasn't ticked, or node just joined).
func (k *KusoClient) NodeHistory(name string) (*resty.Response, error) {
	return k.client.Get("/api/kubernetes/nodes/" + esc(name) + "/history")
}

// SetNodeLabels replaces the kuso-managed labels for a node. Full
// replace — see SetNodeLabelsRequest. Setting `region` also drops a
// matching NoSchedule taint server-side. 204 on success.
func (k *KusoClient) SetNodeLabels(name string, req SetNodeLabelsRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Put("/api/kubernetes/nodes/" + esc(name) + "/labels")
}

// ValidateNode runs the Coolify-style pre-flight check over SSH without
// installing anything. Response: ValidateNodeResult.
func (k *KusoClient) ValidateNode(req ValidateNodeRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/kubernetes/nodes/validate")
}

// JoinNode SSHes into a remote VM and runs the k3s agent install. The
// request blocks for the duration (typically 30-90s). Response:
// {"output": "<install log>", "nodeName": "<name>"}. On a remote-side
// failure the server returns 502 with {"error", "output"}.
func (k *KusoClient) JoinNode(req JoinNodeRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/kubernetes/nodes/join")
}

// RemoveNode cordons → drains → deletes the node, optionally SSHing in
// to uninstall k3s when req.Credentials is set. Response:
// {"removed": "<name>", "uninstallOut": "<log>"}.
func (k *KusoClient) RemoveNode(name string, req RemoveNodeRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/kubernetes/nodes/" + esc(name) + "/remove")
}

// NodeUpdates returns the per-node host package-update advisory.
// Response: {"data": [Advisory, ...]}.
func (k *KusoClient) NodeUpdates() (*resty.Response, error) {
	return k.client.Get("/api/kubernetes/nodes/updates")
}

// ApplyNodeUpdates launches the per-node patch Job. Response (202):
// {"status": "started", "node", "allowReboot"}. 409 when there's
// nothing to do or an apply is already running.
func (k *KusoClient) ApplyNodeUpdates(name string, req ApplyNodeUpdatesRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/kubernetes/nodes/" + esc(name) + "/apply-updates")
}

// CleanupCompleted deletes finished pods + Jobs across namespaces.
// Admin-gated, destructive cluster-wide sweep. Response:
// {"podsDeleted", "jobsDeleted", "namespaces", "errors"}.
func (k *KusoClient) CleanupCompleted() (*resty.Response, error) {
	return k.client.Post("/api/kubernetes/cleanup-completed")
}
