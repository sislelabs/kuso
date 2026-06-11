// Package incidents owns the autonomous incident-response lifecycle: it
// reacts to detection events (pod.crashed / alert.fired / node.unreachable)
// by opening an Incident and spawning an in-cluster agent Job that
// investigates, posts findings, takes operator feedback, and on approval
// opens a fix PR. See docs/superpowers/specs/2026-06-10-incident-agent-design.md.
package incidents

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/lib/pq"

	"kuso/server/internal/db"
	"kuso/server/internal/kube"
	"kuso/server/internal/notify"
)

// Cooldown / max-concurrent / which event types trigger are now runtime
// config (db.IncidentAgentConfig), read via the Manager's ConfigProvider so
// the settings UI can tune them without a redeploy. The defaults live in
// db.DefaultIncidentAgentConfig().

// Spawner abstracts the in-cluster Job launch so the Manager's decision
// logic is testable without kube. jobs.go provides the real impl.
type Spawner interface {
	SpawnInvestigate(ctx context.Context, in db.Incident) (jobName string, err error)
	SpawnImplement(ctx context.Context, in db.Incident) (jobName string, err error)
}

// ConfigProvider supplies the runtime-tunable config (enabled, trigger
// flags, caps). DB-backed + cached so a UI toggle hot-reloads. config.go
// provides the real impl; tests pass a stub.
type ConfigProvider interface {
	Get(ctx context.Context) db.IncidentAgentConfig
}

// Manager reacts to events and drives incidents. Construct with the fields
// set, call Run to start the reaper, and register Hook on the dispatcher.
type Manager struct {
	DB      *db.DB
	Kube    *kube.Client
	Notify  *notify.Dispatcher
	Spawner Spawner
	Config  ConfigProvider // required for gating; nil → feature off
	Logger  *slog.Logger
	// Namespace is the home namespace where KusoProject CRs live, used
	// to read the per-project incident-monitoring opt-in. Empty → "kuso".
	Namespace string
	// now is injected in tests; defaults to time.Now.
	now func() time.Time
}

func (m *Manager) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now().UTC()
}

// cfg returns the live config, or the defaults (disabled) when no provider
// is wired.
func (m *Manager) cfg(ctx context.Context) db.IncidentAgentConfig {
	if m.Config == nil {
		return db.DefaultIncidentAgentConfig() // Enabled:false
	}
	return m.Config.Get(ctx)
}

// projectMonitored reports whether the named project has opted into
// incident monitoring (KusoProject.spec.incidentMonitoring). Fails
// CLOSED — a read error or missing project means "not monitored" so a
// transient apiserver blip can't suddenly enroll every project. The CR
// read is cheap and this runs off the Emit hot path (in handle's
// goroutine), so no caching is needed.
func (m *Manager) projectMonitored(ctx context.Context, project string) bool {
	if m.Kube == nil {
		return false
	}
	ns := m.Namespace
	if ns == "" {
		ns = "kuso"
	}
	p, err := m.Kube.GetKusoProject(ctx, ns, project)
	if err != nil {
		m.log().Warn("incident: could not read project opt-in; treating as not monitored",
			"project", project, "err", err)
		return false
	}
	return p.Spec.IncidentMonitoring
}

func (m *Manager) log() *slog.Logger {
	if m.Logger != nil {
		return m.Logger
	}
	return slog.Default()
}

// triggerEnabled reports whether the config opts this event type in.
func triggerEnabled(cfg db.IncidentAgentConfig, t notify.EventType) bool {
	switch t {
	case notify.EventPodCrashed:
		return cfg.TriggerPod
	case notify.EventAlertFired:
		return cfg.TriggerAlert
	case notify.EventNodeUnreachable:
		return cfg.TriggerNode
	default:
		return false
	}
}

// Hook is registered via dispatcher.SetEventHook. It runs leader-only on
// the Emit path, so it must return fast: it does the cheap DB checks
// inline but hands the (slower) Job spawn to a goroutine. Gated on the live
// config — disabled feature or a disabled trigger type is a no-op.
func (m *Manager) Hook(e notify.Event) {
	if m == nil {
		return
	}
	cfg := m.cfg(context.Background())
	if !cfg.Enabled || !triggerEnabled(cfg, e.Type) {
		return
	}
	// Copy what we need; the event is reused by the caller.
	go m.handle(context.Background(), e)
}

// targetKeyFor is the dedup identity for an event. Pure.
func targetKeyFor(e notify.Event) string {
	svc := e.Service
	if e.Type == notify.EventNodeUnreachable {
		// node.unreachable carries the node name in a field, not Service.
		svc = nodeNameFromEvent(e)
	}
	return string(e.Type) + "|" + e.Project + "|" + svc
}

// nodeNameFromEvent extracts the node name from a node.unreachable event's
// fields (the watcher puts it in a "Node" field). Falls back to Title.
func nodeNameFromEvent(e notify.Event) string {
	for _, f := range e.Fields {
		if f.Name == "Node" || f.Name == "node" {
			return f.Value
		}
	}
	return e.Title
}

// spawnDecision is the pure verdict for an event given the current store
// state. Separating it makes the dedup/cooldown/cap rules unit-testable.
type spawnDecision int

const (
	decideSpawn  spawnDecision = iota // open a new incident + spawn agent
	decideAttach                      // attach to the existing open incident
	decideDrop                        // cooldown or cap — ignore
)

// decide implements the dedup/cooldown/cap rules. open is the current open
// incident for the target (ok=false if none); lastClosed is the most-recent
// close time for the target (ok=false if never closed); openCount is the
// global open count. cooldown + maxConcurrent come from the live config.
func decide(openExists bool, lastClosed time.Time, lastClosedOK bool, openCount, maxConcurrent int, cooldown time.Duration, now time.Time) spawnDecision {
	if openExists {
		return decideAttach
	}
	if lastClosedOK && cooldown > 0 && now.Sub(lastClosed) < cooldown {
		return decideDrop
	}
	if maxConcurrent > 0 && openCount >= maxConcurrent {
		return decideDrop
	}
	return decideSpawn
}

// handle runs the dedup decision and acts on it.
func (m *Manager) handle(ctx context.Context, e notify.Event) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	key := targetKeyFor(e)
	log := m.log().With("event", string(e.Type), "target", key)
	cfg := m.cfg(ctx)

	// Per-project opt-in. The agent only investigates project-scoped
	// events (pod crash, alert) for projects that have explicitly opted
	// into incident monitoring (KusoProject.spec.incidentMonitoring).
	// Node-level events are cluster-wide (no owning project) and stay
	// gated only by the global TriggerNode flag — filtering them by a
	// project opt-in would silently drop every node incident.
	if e.Project != "" && !m.projectMonitored(ctx, e.Project) {
		log.Info("incident: event skipped (project not opted into incident monitoring)", "project", e.Project)
		return
	}

	open, openErr := m.DB.OpenIncidentForTarget(ctx, key)
	openExists := openErr == nil
	lastClosed, lastOK, _ := m.DB.LastClosedAtForTarget(ctx, key)
	openCount, _ := m.DB.CountOpenIncidents(ctx)

	cooldown := time.Duration(cfg.CooldownHours) * time.Hour
	switch decide(openExists, lastClosed, lastOK, openCount, cfg.MaxConcurrent, cooldown, m.clock()) {
	case decideAttach:
		// A matching event while an incident is open: log it as feedback so
		// the agent/operator see the issue recurred. Don't spawn.
		_ = m.DB.AppendIncidentFeedback(ctx, open.ID, db.IncidentFeedback{
			Text: "recurred: " + e.Title,
		})
		log.Info("incident: event attached to open incident", "id", open.ID)
	case decideDrop:
		log.Info("incident: event dropped (cooldown or concurrency cap)")
	case decideSpawn:
		m.openAndSpawn(ctx, e, key)
	}
}

// openAndSpawn creates the Incident row and launches the investigate Job.
func (m *Manager) openAndSpawn(ctx context.Context, e notify.Event, key string) {
	id := newID("inc")
	pack := contextPack(e)
	in := db.Incident{
		ID:          id,
		EventType:   string(e.Type),
		Project:     e.Project,
		Service:     serviceOrNode(e),
		TargetKey:   key,
		State:       db.IncidentInvestigating,
		Title:       e.Title,
		Severity:    severityOr(e.Severity, "warn"),
		ContextPack: pack,
		AgentToken:  newToken(), // 128-bit random bearer for the agent callbacks
	}
	if err := m.DB.CreateIncident(ctx, in); err != nil {
		// A unique-violation on the partial "one open incident per target"
		// index means a concurrent event already opened this incident (the
		// TOCTOU guard). That's success, not failure — drop quietly.
		if isUniqueViolation(err) {
			m.log().Info("incident: concurrent open for target — attached", "target", key)
			return
		}
		m.log().Error("incident: create row", "err", err)
		return
	}
	if m.Spawner == nil {
		m.log().Warn("incident: no spawner configured; incident opened but no agent", "id", id)
		return
	}
	job, err := m.Spawner.SpawnInvestigate(ctx, in)
	if err != nil {
		m.log().Error("incident: spawn investigate job", "id", id, "err", err)
		// Leave the incident open; a retry path / operator can re-trigger.
		return
	}
	_ = m.DB.SetIncidentJob(ctx, id, "investigate", job)
	m.log().Info("incident: opened + investigate spawned", "id", id, "job", job)
}

// SpawnImplementFor is called by the HTTP layer when the operator approves
// ("go"). It moves the incident to implementing and launches the Job.
func (m *Manager) SpawnImplementFor(ctx context.Context, id string) error {
	in, err := m.DB.GetIncident(ctx, id)
	if err != nil {
		return err
	}
	if m.Spawner == nil {
		return fmt.Errorf("incidents: no spawner configured")
	}
	if err := m.DB.SetIncidentState(ctx, id, db.IncidentImplementing); err != nil {
		return err
	}
	in.State = db.IncidentImplementing
	job, err := m.Spawner.SpawnImplement(ctx, in)
	if err != nil {
		return fmt.Errorf("spawn implement job: %w", err)
	}
	return m.DB.SetIncidentJob(ctx, id, "implement", job)
}

// --- helpers ---

func serviceOrNode(e notify.Event) string {
	if e.Type == notify.EventNodeUnreachable {
		return nodeNameFromEvent(e)
	}
	return e.Service
}

func severityOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// contextPack serializes the event into the JSON blob handed to the agent.
func contextPack(e notify.Event) json.RawMessage {
	type pack struct {
		Type           string            `json:"type"`
		Title          string            `json:"title"`
		Body           string            `json:"body,omitempty"`
		Project        string            `json:"project,omitempty"`
		Service        string            `json:"service,omitempty"`
		Severity       string            `json:"severity,omitempty"`
		LogTail        string            `json:"logTail,omitempty"`
		Fields         map[string]string `json:"fields,omitempty"`
		Classification any               `json:"classification,omitempty"`
	}
	p := pack{
		Type: string(e.Type), Title: e.Title, Body: e.Body,
		Project: e.Project, Service: e.Service, Severity: e.Severity,
		LogTail: e.LogTail, Classification: e.Classification,
	}
	if len(e.Fields) > 0 {
		p.Fields = map[string]string{}
		for _, f := range e.Fields {
			p.Fields[f.Name] = f.Value
		}
	}
	b, err := json.Marshal(p)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

// newID returns prefix + 8 random hex chars (32 bits). Used for incident
// ids (collision-tolerant: a dup id just fails the PK insert).
func newID(prefix string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return prefix + "-" + hex.EncodeToString(b)
}

// newToken returns a 128-bit random hex bearer token (32 hex chars). Used
// as the per-incident agent token — the only credential gating the
// /findings + /pr callbacks, so it gets full entropy.
func newToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// isUniqueViolation reports whether err is a Postgres unique-constraint
// violation (SQLSTATE 23505). Used to treat a concurrent open-incident
// insert as already-handled rather than a hard error.
func isUniqueViolation(err error) bool {
	var pqe *pq.Error
	if errors.As(err, &pqe) {
		return pqe.Code == "23505"
	}
	return false
}
