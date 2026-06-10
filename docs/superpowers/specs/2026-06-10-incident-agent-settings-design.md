# Incident Agent settings page — design

**Date:** 2026-06-10
**Status:** approved, implementing

## Goal

A `/settings/incident-agent` page to **enable/configure** the autonomous
incident-response agent from the UI — replacing the current "env vars +
hand-set cluster secrets" config. Admin-only. Hot-reloads (toggle takes effect
in seconds, no redeploy). Handles both plain knobs AND secret upload (CC creds,
Discord). Follows the established settings pattern (build-settings + backups).

## Config split (three storage tiers)

| Config | Storage | UI |
|---|---|---|
| enabled, trigger flags (pod/alert/node), maxConcurrent, cooldownHours, agentImage | `Setting` kv (keys `incident.*`) | editable form, hot-reloaded |
| Claude Code creds | k8s Secret `kuso-incident-agent-cc` (key credentials.json) | write-only paste/CLI; shows "configured ✓ · expires X" |
| Discord bot token + kuso bot token + channel id | Secret `kuso-incident-bot-secrets` + ConfigMap `kuso-incident-bot-config` | write-only; shows "configured ✓ / not set" |

## Backend

### `db.IncidentAgentConfig` (mirrors BuildSettings)

```go
type IncidentAgentConfig struct {
    Enabled       bool   `json:"enabled"`
    TriggerPod    bool   `json:"triggerPod"`
    TriggerAlert  bool   `json:"triggerAlert"`
    TriggerNode   bool   `json:"triggerNode"`
    MaxConcurrent int    `json:"maxConcurrent"`
    CooldownHours int    `json:"cooldownHours"`
    AgentImage    string `json:"agentImage,omitempty"`
}
func DefaultIncidentAgentConfig() IncidentAgentConfig // enabled:false, all triggers on, max 3, cooldown 1h
func (d *DB) GetIncidentAgentConfig(ctx) (IncidentAgentConfig, error)   // merge defaults + incident.* keys
func (d *DB) SetIncidentAgentConfig(ctx, cfg, updatedBy) error          // upsert incident.* keys
func (d *DB) IncidentAgentConfigExists(ctx) (bool, error)              // for seed-once
```
Keys: `incident.enabled`, `incident.triggerPod`, `incident.triggerAlert`,
`incident.triggerNode`, `incident.maxConcurrent`, `incident.cooldownHours`,
`incident.agentImage`. JSON-quoted TEXT values (quote/unquote shim).

### Manager: `ConfigProvider` seam (replaces hardcoded constants)

```go
type ConfigProvider interface { Get(ctx context.Context) db.IncidentAgentConfig }
```
- `dbConfigProvider` wraps `*db.DB` with a ~30s cache (mirrors notify's notifsCache);
  `Invalidate()` clears it. The settings handler's `OnSettingsChange` hook calls it.
- `Manager.Hook`: returns early when `!cfg.Enabled` OR the event type's trigger flag
  is off (replaces the static `triggerEventTypes` map).
- `decide()` / `handle()`: take `cfg.MaxConcurrent` and
  `time.Duration(cfg.CooldownHours)*time.Hour` instead of the `DefaultMaxConcurrent`
  / `Cooldown` constants (kept as fallback defaults in DefaultIncidentAgentConfig).
- The spawner's `AgentImage`: when `cfg.AgentImage != ""`, the Manager passes it
  through (the KubeSpawner already has an AgentImage field; the Manager updates it
  or the spawner reads from the provider).
- **Manager is now ALWAYS constructed** in main.go (not gated on the env). The env
  `KUSO_INCIDENT_AGENT` only seeds the DB on first boot.

### Seeding (main.go)

On boot: if `!IncidentAgentConfigExists`, write `DefaultIncidentAgentConfig()` with
`Enabled = (KUSO_INCIDENT_AGENT == "true")`, `AgentImage = KUSO_INCIDENT_AGENT_IMAGE`.
So existing env-configured installs keep their on/off; thereafter the UI owns it.

### HTTP — `IncidentAgentSettingsHandler` (`/api/admin/settings/incident-agent`)

All `settings:admin`. Mounted in router.
- `GET /api/admin/settings/incident-agent` → `{config, status}`. status computed
  server-side, NEVER echoes secret values:
  `{ccConfigured bool, ccExpiresAt string, ccSubscriptionType string, discordConfigured bool, channelConfigured bool, botDeployed bool, botReady bool, openIncidents int}`.
- `PUT /api/admin/settings/incident-agent` → save knobs (validate maxConcurrent 1..50,
  cooldownHours 0..168), then invalidate the provider cache (hot-reload).
- `PUT /api/admin/settings/incident-agent/cc-credentials` → body `{credentials: "<json>"}`;
  server validates it parses + has `claudeAiOauth.accessToken`, writes Secret
  `kuso-incident-agent-cc` key credentials.json (Create-or-Update, like backups S3).
- `PUT /api/admin/settings/incident-agent/discord` → body
  `{botToken?, channelId?, kusoBotToken?}` (each optional — only-set fields update);
  writes the bot secret + configmap. Restarts the bot deployment so it reconnects.

Secret presence/metadata read by Get-ing the secrets + parsing JSON (expiry,
subscriptionType from the CC blob); values never leave the server.

## Frontend

- `web/src/features/incident-agent/{api,hooks,index}.ts` — typed client +
  React Query hooks (getConfig, putConfig, putCCCreds, putDiscord).
- `web/src/app/(app)/settings/incident-agent/page.tsx` — sections:
  1. **Master toggle** (Enabled) + a status banner (enabled/disabled, open incidents).
  2. **Triggers** — three checkboxes (pod crash / alert / node down).
  3. **Limits** — maxConcurrent + cooldownHours inputs; agentImage override.
  4. **Claude Code credentials** — write-only paste textarea + Save; shows
     "configured ✓ · max sub · expires <date>" or "not configured" + the
     `kuso incident-agent set-credentials` hint.
  5. **Discord** — write-only bot-token + kuso-bot-token fields + channel-id input +
     Save; shows "bot connected ✓" / "not configured".
  - Mirror `settings/alerts` + `settings/builds` styling (Card sections, Skeleton,
    toast, useCan(Perms.SettingsAdmin)).
- Add a tile to `settings/page.tsx` (group "integrations"):
  `{ href:"/settings/incident-agent", title:"Incident agent", description:"Autonomous claude -p agent that investigates incidents + opens fix PRs.", icon:Bot, perm:SettingsAdmin, keywords:"incident agent claude ai discord pr autonomous crash alert" }`.

## CLI

`kuso incident-agent set-credentials` (already stubbed): extract the
`claudeAiOauth` block from the macOS Keychain (`security find-generic-password -s
"Claude Code-credentials" -w`) and PUT it to `/cc-credentials`. Falls back to a
`--file <path>` flag on non-mac.

## Security

- Every endpoint `settings:admin` (the kuso-admins group).
- Secrets are WRITE-ONLY through the API: a PUT sets them, but GET never returns
  the value — only presence + non-secret metadata (expiry, sub type, channel id is
  non-secret so it can show).
- Server validates the CC blob shape before storing (reject garbage that would just
  make every agent run fail auth).
- The Discord/CC tokens still live in k8s Secrets exactly as today; the only change
  is they can now be SET via the admin API instead of only kubectl.
- Pasting a token into a browser form is the accepted tradeoff (admin already has
  cluster-admin-equivalent power; documented).

## Out of scope (v1)

Per-event-type advanced config (per-project enable, severity thresholds),
rotating/viewing the kuso bot token from the UI, multi-channel Discord,
editing the agent prompt. The page is the operator console for the existing
feature, not new agent capabilities.

## Testing

- db: Get/SetIncidentAgentConfig round-trip + defaults merge (PG-gated).
- Manager: ConfigProvider gates Hook (disabled → no spawn; trigger-off → no spawn);
  decide() honors cfg cap/cooldown. Pure/fake-provider unit tests.
- handlers: admin gating; CC-blob validation (reject non-claudeAiOauth); status
  computed without echoing values; PUT invalidates cache.
- Live: toggle off in UI → next crash doesn't spawn; toggle on → it does. Paste
  creds → agent authenticates.
