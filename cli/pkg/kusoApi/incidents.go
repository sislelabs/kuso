package kusoApi

import (
	"time"

	"github.com/go-resty/resty/v2"
)

// IncidentFeedback mirrors server-go db.IncidentFeedback — one operator
// message in the feedback log. Exactly one of Text / Decision is set;
// Decision ∈ {"go","reject"}.
type IncidentFeedback struct {
	At       time.Time `json:"at"`
	Text     string    `json:"text,omitempty"`
	Decision string    `json:"decision,omitempty"`
}

// Incident mirrors the subset of server-go db.Incident the CLI renders.
// AgentToken is intentionally absent — the server never serializes it.
type Incident struct {
	ID            string             `json:"id"`
	EventType     string             `json:"eventType"`
	Project       string             `json:"project"`
	Service       string             `json:"service"`
	TargetKey     string             `json:"targetKey"`
	State         string             `json:"state"`
	Title         string             `json:"title"`
	Severity      string             `json:"severity"`
	Findings      string             `json:"findings"`
	Feedback      []IncidentFeedback `json:"feedback"`
	DiscordThread string             `json:"discordThread"`
	PRUrl         string             `json:"prUrl"`
	PRNumber      int                `json:"prNumber"`
	CreatedAt     time.Time          `json:"createdAt"`
	UpdatedAt     time.Time          `json:"updatedAt"`
	ClosedAt      *time.Time         `json:"closedAt,omitempty"`
}

// ListIncidents returns the newest incidents (UI/feed list).
func (k *KusoClient) ListIncidents() (*resty.Response, error) {
	return k.client.Get("/api/incidents")
}

// GetIncident returns one incident by id.
func (k *KusoClient) GetIncident(id string) (*resty.Response, error) {
	return k.client.Get("/api/incidents/" + esc(id))
}

// ResolveIncident closes an incident (operator action).
func (k *KusoClient) ResolveIncident(id string) (*resty.Response, error) {
	return k.client.Post("/api/incidents/" + esc(id) + "/resolve")
}

// PutIncidentAgentCCCredentials uploads the Claude Code credentials blob to
// the incident-agent settings endpoint (settings:admin). The server validates
// the claudeAiOauth shape + stores it in the kuso-incident-agent-cc secret.
func (k *KusoClient) PutIncidentAgentCCCredentials(credentials string) (*resty.Response, error) {
	k.client.SetBody(map[string]string{"credentials": credentials})
	return k.client.Put("/api/admin/settings/incident-agent/cc-credentials")
}
