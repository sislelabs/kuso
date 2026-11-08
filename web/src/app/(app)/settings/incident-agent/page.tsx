"use client";

import { Switch } from "@/components/ui/switch";
import { useEffect, useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, AlertTriangle, Search } from "lucide-react";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useCan, Perms } from "@/features/auth";
import {
  getIncidentAgentSettings,
  putIncidentAgentConfig,
  putCCCredentials,
  putDiscordConfig,
  type IncidentAgentConfig,
} from "@/features/incident-agent";
import { useProjects, useSetIncidentMonitoring } from "@/features/projects";
import { IncidentsSection } from "@/components/incidents/IncidentsSection";
import { toast } from "sonner";
import { cn } from "@/lib/utils";
import { QueryErrorState } from "@/components/shared/QueryErrorState";
import { SettingsPage, SettingsPageLoading } from "@/components/settings/SettingsPage";
import { SettingsField, SettingsNotice, SettingsSection } from "@/components/settings/SettingsSection";

const DESCRIPTION = (
  <>
    An autonomous <code className="font-mono text-xs">claude -p</code> agent that investigates
    incidents, posts findings to Discord, and opens fix PRs on your approval.
  </>
);

export default function IncidentAgentPage() {
  const qc = useQueryClient();
  const canAdmin = useCan(Perms.SettingsAdmin);
  const q = useQuery({ queryKey: ["incident-agent"], queryFn: getIncidentAgentSettings });

  const invalidate = () => qc.invalidateQueries({ queryKey: ["incident-agent"] });

  if (q.isPending) return <SettingsPageLoading title="Incident agent" rows={3} />;
  if (q.isError)
    return (
      <SettingsPage title="Incident agent" description={DESCRIPTION}>
        <QueryErrorState what="incident agent settings" error={q.error} onRetry={() => void q.refetch()} />
      </SettingsPage>
    );

  const { config, status } = q.data!;
  const readOnly = !canAdmin;

  return (
    <SettingsPage title="Incident agent" description={DESCRIPTION}>
      {readOnly && (
        <SettingsNotice tone="neutral">
          You don&apos;t have <span className="font-mono">settings:admin</span>. This page is read-only.
        </SettingsNotice>
      )}

      <ConfigSection config={config} status={status} readOnly={readOnly} onSaved={invalidate} />
      <IncidentsSection canAdmin={canAdmin} />
      <MonitoredProjectsSection agentEnabled={config.enabled} readOnly={readOnly} />
      <CredentialsSection status={status} readOnly={readOnly} onSaved={invalidate} />
      <DiscordSection status={status} readOnly={readOnly} onSaved={invalidate} />
    </SettingsPage>
  );
}

// --- per-project opt-in: which projects the agent is allowed to watch ---
function MonitoredProjectsSection({
  agentEnabled,
  readOnly,
}: {
  agentEnabled: boolean;
  readOnly: boolean;
}) {
  const projects = useProjects();
  const setMonitoring = useSetIncidentMonitoring();
  const [filter, setFilter] = useState("");
  // Track the project currently being toggled so we can spin just that row.
  const [pending, setPending] = useState<string | null>(null);

  const rows = (projects.data ?? [])
    .map((p) => ({ name: p.metadata.name, on: p.spec.incidentMonitoring === true }))
    .filter((r) => r.name.toLowerCase().includes(filter.toLowerCase()))
    .sort((a, b) => a.name.localeCompare(b.name));
  const monitoredCount = (projects.data ?? []).filter(
    (p) => p.spec.incidentMonitoring === true,
  ).length;

  const toggle = (name: string, enabled: boolean) => {
    setPending(name);
    setMonitoring.mutate(
      { name, enabled },
      {
        onSuccess: () =>
          toast.success(`${name} ${enabled ? "now monitored" : "no longer monitored"}`),
        onError: (e) => toast.error(e instanceof Error ? e.message : "update failed"),
        onSettled: () => setPending(null),
      },
    );
  };

  return (
    <SettingsSection
      title="Monitored projects"
      description={
        <>
          Opt-in, per project. The agent only investigates crashes and alerts for projects toggled
          on here, so a noisy or low-priority project never burns an agent run.
        </>
      }
      actions={
        <span className="rounded-full border border-[var(--border-subtle)] bg-[var(--bg-tertiary)] px-2 py-0.5 text-xs text-[var(--text-secondary)]">
          {monitoredCount} of {projects.data?.length ?? 0} enrolled
        </span>
      }
    >
      {!agentEnabled && (
        <SettingsNotice tone="info">
          The agent is disabled above, so nothing fires regardless of these toggles.
        </SettingsNotice>
      )}

      {(projects.data?.length ?? 0) > 8 && (
        <div className="relative">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-[var(--text-tertiary)]" />
          <Input
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            placeholder="Filter projects…"
            aria-label="Filter projects"
            className="pl-8"
          />
        </div>
      )}

      {projects.isPending ? (
        <Skeleton className="h-24" />
      ) : rows.length === 0 ? (
        <p className="py-4 text-center text-sm text-[var(--text-secondary)]">
          {filter ? "No projects match." : "No projects yet."}
        </p>
      ) : (
        <ul className="max-h-[28rem] divide-y divide-[var(--border-subtle)] overflow-y-auto rounded-sm border border-[var(--border-subtle)]">
          {rows.map((r) => (
            <li
              key={r.name}
              className="flex items-center justify-between gap-3 bg-[var(--bg-primary)] px-3 py-2"
            >
              <div className="flex min-w-0 items-center gap-2">
                <span
                  aria-hidden
                  className={cn(
                    "h-1.5 w-1.5 flex-shrink-0 rounded-full",
                    r.on ? "bg-[var(--success)]" : "bg-[var(--text-tertiary)]/40",
                  )}
                />
                <span className="truncate font-mono text-sm text-[var(--text-primary)]">
                  {r.name}
                </span>
              </div>
              <ProjectMonitorToggle
                label={r.name}
                on={r.on}
                disabled={readOnly || pending === r.name}
                busy={pending === r.name}
                onChange={(v) => toggle(r.name, v)}
              />
            </li>
          ))}
        </ul>
      )}
    </SettingsSection>
  );
}

// ProjectMonitorToggle is a compact on/off switch for one project row.
function ProjectMonitorToggle({
  label,
  on,
  disabled,
  busy,
  onChange,
}: {
  label: string;
  on: boolean;
  disabled: boolean;
  busy: boolean;
  onChange: (v: boolean) => void;
}) {
  return (
    <Switch
      on={on}
      onChange={onChange}
      disabled={disabled}
      label={`Monitor ${label}`}
      className={cn(busy && "animate-pulse")}
    />
  );
}

// --- master toggle + triggers + limits ---
function ConfigSection({
  config,
  status,
  readOnly,
  onSaved,
}: {
  config: IncidentAgentConfig;
  status: { openIncidents: number };
  readOnly: boolean;
  onSaved: () => void;
}) {
  const [draft, setDraft] = useState<IncidentAgentConfig>(config);
  useEffect(() => setDraft(config), [config]);

  const save = useMutation({
    mutationFn: () => putIncidentAgentConfig(draft),
    onSuccess: () => {
      toast.success("Saved — applies within ~30s");
      onSaved();
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "save failed"),
  });

  const dirty = JSON.stringify(draft) !== JSON.stringify(config);
  const set = (patch: Partial<IncidentAgentConfig>) => setDraft((d) => ({ ...d, ...patch }));

  return (
    <SettingsSection
      title={
        <span className="flex items-center gap-2">
          {draft.enabled ? (
            <CheckCircle2 className="h-4 w-4 text-[var(--success)]" />
          ) : (
            <AlertTriangle className="h-4 w-4 text-[var(--text-tertiary)]" />
          )}
          {draft.enabled ? "Agent enabled" : "Agent disabled"}
        </span>
      }
      description={
        <span className="inline-flex items-center gap-1.5">
          <span
            aria-hidden
            className={cn(
              "h-1.5 w-1.5 rounded-full",
              status.openIncidents > 0 ? "bg-[var(--warning)]" : "bg-[var(--success)]",
            )}
          />
          {status.openIncidents} open incident{status.openIncidents === 1 ? "" : "s"} right now.
        </span>
      }
      actions={
        <span className="flex items-center gap-2 text-sm text-[var(--text-secondary)]">
          <Switch
            on={draft.enabled}
            disabled={readOnly}
            onChange={(v) => set({ enabled: v })}
            labelledBy="incident-agent-enabled"
          />
          <span id="incident-agent-enabled">{draft.enabled ? "Enabled" : "Disabled"}</span>
        </span>
      }
      footerHint="Changes apply within ~30s."
      footer={
        !readOnly && (
          <Button size="sm" disabled={!dirty || save.isPending} onClick={() => save.mutate()}>
            {save.isPending ? "Saving…" : "Save"}
          </Button>
        )
      }
    >
      <fieldset className="space-y-2" disabled={readOnly}>
        <legend className="mb-2 text-sm font-medium text-[var(--text-primary)]">Auto-trigger on</legend>
        <Toggle label="Pod crashes (CrashLoopBackOff, OOM, ImagePull)" checked={draft.triggerPod} onChange={(v) => set({ triggerPod: v })} />
        <Toggle label="Alert rules firing (CPU, memory, disk, log match)" checked={draft.triggerAlert} onChange={(v) => set({ triggerAlert: v })} />
        <Toggle label="Node down (NotReady for more than 5 min)" checked={draft.triggerNode} onChange={(v) => set({ triggerNode: v })} />
      </fieldset>

      <div className="grid grid-cols-1 gap-5 sm:grid-cols-2">
        <NumField
          label="Max concurrent agents"
          hint="0 = no cap. Bounds Claude Code subscription usage."
          value={draft.maxConcurrent}
          disabled={readOnly}
          onChange={(n) => set({ maxConcurrent: n })}
        />
        <NumField
          label="Cooldown (hours)"
          hint="0 = none. Suppresses re-triggers after an incident closes."
          value={draft.cooldownHours}
          disabled={readOnly}
          onChange={(n) => set({ cooldownHours: n })}
        />
      </div>

      <SettingsField label="Agent image" hint="Optional override of the agent container image.">
        <Input
          value={draft.agentImage ?? ""}
          disabled={readOnly}
          placeholder="ghcr.io/sislelabs/kuso-incident-agent:latest"
          onChange={(e) => set({ agentImage: e.target.value })}
          className="font-mono"
        />
      </SettingsField>
    </SettingsSection>
  );
}

// --- Claude Code credentials (write-only) ---
function CredentialsSection({
  status,
  readOnly,
  onSaved,
}: {
  status: { ccConfigured: boolean; ccExpiresAt?: string; ccSubscriptionType?: string };
  readOnly: boolean;
  onSaved: () => void;
}) {
  const [creds, setCreds] = useState("");
  const save = useMutation({
    mutationFn: () => putCCCredentials(creds),
    onSuccess: () => {
      toast.success("Claude Code credentials stored");
      setCreds("");
      onSaved();
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "save failed"),
  });

  return (
    <SettingsSection
      title="Claude Code credentials"
      description={
        <>
          The agent runs as <em>you</em>, using your Claude Code subscription. Paste your{" "}
          <code className="font-mono text-xs">~/.claude/.credentials.json</code> (the{" "}
          <code className="font-mono text-xs">claudeAiOauth</code> blob), or run{" "}
          <code className="font-mono text-xs">kuso incident-agent set-credentials</code> to upload it
          from your Keychain.
        </>
      }
      footerHint="Write-only: stored credentials are never shown back."
      footer={
        !readOnly && (
          <Button size="sm" disabled={!creds.trim() || save.isPending} onClick={() => save.mutate()}>
            {save.isPending ? "Saving…" : "Save credentials"}
          </Button>
        )
      }
    >
      <StatusLine
        ok={status.ccConfigured}
        okText={`Configured${status.ccSubscriptionType ? ` · ${status.ccSubscriptionType} subscription` : ""}${
          status.ccExpiresAt ? ` · expires ${new Date(status.ccExpiresAt).toLocaleString()}` : ""
        }`}
        badText="Not configured. The agent can't authenticate."
      />
      {!readOnly && (
        <SettingsField label="Credentials JSON">
          <textarea
            value={creds}
            onChange={(e) => setCreds(e.target.value)}
            rows={4}
            spellCheck={false}
            aria-label="Credentials JSON"
            placeholder='{"claudeAiOauth":{"accessToken":"...","refreshToken":"...","expiresAt":...,"subscriptionType":"max"}}'
            className="w-full resize-y rounded-sm border border-border bg-input px-3 py-2 font-mono text-xs outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50"
          />
        </SettingsField>
      )}
    </SettingsSection>
  );
}

// --- Discord bridge (write-only token + channel) ---
function DiscordSection({
  status,
  readOnly,
  onSaved,
}: {
  status: { discordConfigured: boolean; channelId?: string; botDeployed: boolean; botReady: boolean };
  readOnly: boolean;
  onSaved: () => void;
}) {
  const [botToken, setBotToken] = useState("");
  const [kusoBotToken, setKusoBotToken] = useState("");
  const [channelId, setChannelId] = useState(status.channelId ?? "");
  useEffect(() => setChannelId(status.channelId ?? ""), [status.channelId]);

  const save = useMutation({
    mutationFn: () =>
      putDiscordConfig({
        botToken: botToken || undefined,
        kusoBotToken: kusoBotToken || undefined,
        // "" clears the stored channel; undefined keeps it.
        channelId: channelId.trim() !== (status.channelId ?? "") ? channelId.trim() : undefined,
      }),
    onSuccess: () => {
      toast.success("Discord config saved — bot restarting");
      setBotToken("");
      setKusoBotToken("");
      onSaved();
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "save failed"),
  });

  const dirty = botToken !== "" || kusoBotToken !== "" || channelId !== (status.channelId ?? "");

  return (
    <SettingsSection
      title="Discord bridge"
      description="The bot posts findings to a channel and relays your replies and reactions (✅ = go) back to the agent. It needs the MESSAGE CONTENT intent plus Send Messages and Create Public Threads on the channel."
      footerHint="Tokens are write-only. Saving restarts the bot."
      footer={
        !readOnly && (
          <Button size="sm" disabled={!dirty || save.isPending} onClick={() => save.mutate()}>
            {save.isPending ? "Saving…" : "Save Discord config"}
          </Button>
        )
      }
    >
      <StatusLine
        ok={status.discordConfigured && status.botReady}
        okText={`Bot connected${status.channelId ? ` · channel ${status.channelId}` : ""}`}
        badText={
          !status.discordConfigured
            ? "Not configured."
            : !status.botDeployed
              ? "Configured, but the bot deployment isn't running (apply deploy/incident-bot.yaml)."
              : "Configured, but the bot isn't connected yet."
        }
      />
      {!readOnly && (
        <>
          <div className="grid grid-cols-1 gap-5 sm:grid-cols-2">
            <Field label="Discord bot token" value={botToken} onChange={setBotToken} placeholder="Unchanged" secret />
            <Field label="kuso bot token" hint="An admin API token the bot uses to call kuso." value={kusoBotToken} onChange={setKusoBotToken} placeholder="Unchanged" secret />
          </div>
          <Field label="Channel ID" value={channelId} onChange={setChannelId} placeholder="e.g. 123456789012345678" />
        </>
      )}
    </SettingsSection>
  );
}

// --- small shared bits ---
function Toggle({ label, checked, onChange }: { label: string; checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <div className="flex items-center gap-2.5 text-sm text-[var(--text-secondary)]">
      <Switch on={checked} onChange={onChange} label={label} size="sm" />
      <span aria-hidden className="cursor-pointer" onClick={() => onChange(!checked)}>
        {label}
      </span>
    </div>
  );
}

function NumField({
  label,
  hint,
  value,
  disabled,
  onChange,
}: {
  label: string;
  hint: string;
  value: number;
  disabled: boolean;
  onChange: (n: number) => void;
}) {
  return (
    <SettingsField label={label} hint={hint}>
      <Input
        type="number"
        value={value}
        disabled={disabled}
        aria-label={label}
        onChange={(e) => onChange(parseInt(e.target.value || "0", 10))}
        className="font-mono"
      />
    </SettingsField>
  );
}

function Field({
  label,
  hint,
  value,
  onChange,
  placeholder,
  secret,
}: {
  label: string;
  hint?: string;
  value: string;
  onChange: (v: string) => void;
  placeholder?: string;
  secret?: boolean;
}) {
  return (
    <SettingsField label={label} hint={hint}>
      <Input
        type={secret ? "password" : "text"}
        value={value}
        placeholder={placeholder}
        aria-label={label}
        onChange={(e) => onChange(e.target.value)}
        className="font-mono"
      />
    </SettingsField>
  );
}

function StatusLine({ ok, okText, badText }: { ok: boolean; okText: string; badText: string }) {
  return (
    <div
      className={cn(
        "flex items-start gap-2 rounded-sm border px-3 py-2 text-sm",
        ok
          ? "border-[var(--success)]/30 bg-[var(--success-subtle)] text-[var(--text-primary)]"
          : "border-[var(--warning)]/30 bg-[var(--warning-subtle)] text-[var(--text-primary)]",
      )}
    >
      {ok ? (
        <CheckCircle2 className="mt-0.5 h-4 w-4 shrink-0 text-[var(--success)]" />
      ) : (
        <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-[var(--warning)]" />
      )}
      <span className="min-w-0 break-words">{ok ? okText : badText}</span>
    </div>
  );
}
