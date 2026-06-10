"use client";

import { useEffect, useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { Bot, ShieldCheck, AlertTriangle, MessageSquare } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
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
import { toast } from "sonner";
import { relativeTime } from "@/lib/format";

export default function IncidentAgentPage() {
  const qc = useQueryClient();
  const canAdmin = useCan(Perms.SettingsAdmin);
  const q = useQuery({ queryKey: ["incident-agent"], queryFn: getIncidentAgentSettings });

  const invalidate = () => qc.invalidateQueries({ queryKey: ["incident-agent"] });

  if (q.isPending) return <Skeleton className="m-6 h-96" />;
  if (q.isError)
    return (
      <p className="m-6 font-mono text-sm text-red-400">
        {q.error instanceof Error ? q.error.message : "failed to load"}
      </p>
    );

  const { config, status } = q.data!;
  const readOnly = !canAdmin;

  return (
    <div className="mx-auto max-w-2xl space-y-5 p-6">
      <header className="flex items-center gap-3">
        <Bot className="h-6 w-6 text-[var(--accent)]" />
        <div>
          <h1 className="text-lg font-semibold text-[var(--text-primary)]">Incident agent</h1>
          <p className="text-xs text-[var(--text-tertiary)]">
            An autonomous <code className="font-mono">claude -p</code> agent that investigates
            incidents, posts findings to Discord, and opens fix PRs on your approval.
          </p>
        </div>
      </header>

      <ConfigSection config={config} status={status} readOnly={readOnly} onSaved={invalidate} />
      <CredentialsSection status={status} readOnly={readOnly} onSaved={invalidate} />
      <DiscordSection status={status} readOnly={readOnly} onSaved={invalidate} />
    </div>
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
    <Card>
      <CardHeader className="flex-row items-center justify-between">
        <CardTitle className="flex items-center gap-2 text-sm">
          {draft.enabled ? (
            <ShieldCheck className="h-4 w-4 text-emerald-400" />
          ) : (
            <AlertTriangle className="h-4 w-4 text-[var(--text-tertiary)]" />
          )}
          {draft.enabled ? "Enabled" : "Disabled"}
        </CardTitle>
        <label className="flex cursor-pointer items-center gap-2 text-xs">
          <input
            type="checkbox"
            checked={draft.enabled}
            disabled={readOnly}
            onChange={(e) => set({ enabled: e.target.checked })}
          />
          turn {draft.enabled ? "off" : "on"}
        </label>
      </CardHeader>
      <CardContent className="space-y-4 text-[13px]">
        <div className="text-[11px] text-[var(--text-tertiary)]">
          {status.openIncidents} open incident{status.openIncidents === 1 ? "" : "s"} right now.
        </div>

        <fieldset className="space-y-1.5" disabled={readOnly}>
          <legend className="mb-1 font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
            Auto-trigger on
          </legend>
          <Toggle label="Pod crashes (CrashLoopBackOff / OOM / ImagePull)" checked={draft.triggerPod} onChange={(v) => set({ triggerPod: v })} />
          <Toggle label="Alert rules firing (CPU / mem / disk / log match)" checked={draft.triggerAlert} onChange={(v) => set({ triggerAlert: v })} />
          <Toggle label="Node down (NotReady > 5 min)" checked={draft.triggerNode} onChange={(v) => set({ triggerNode: v })} />
        </fieldset>

        <div className="grid grid-cols-2 gap-3">
          <NumField
            label="Max concurrent agents"
            hint="0 = no cap. Bounds CC-sub usage."
            value={draft.maxConcurrent}
            disabled={readOnly}
            onChange={(n) => set({ maxConcurrent: n })}
          />
          <NumField
            label="Cooldown (hours)"
            hint="0 = none. Suppress re-trigger after an incident closes."
            value={draft.cooldownHours}
            disabled={readOnly}
            onChange={(n) => set({ cooldownHours: n })}
          />
        </div>

        <div>
          <label className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
            Agent image (optional override)
          </label>
          <Input
            value={draft.agentImage ?? ""}
            disabled={readOnly}
            placeholder="ghcr.io/sislelabs/kuso-incident-agent:latest"
            onChange={(e) => set({ agentImage: e.target.value })}
            className="mt-1 font-mono text-xs"
          />
        </div>

        {!readOnly && (
          <div className="flex justify-end">
            <Button size="sm" disabled={!dirty || save.isPending} onClick={() => save.mutate()}>
              {save.isPending ? "Saving…" : "Save"}
            </Button>
          </div>
        )}
      </CardContent>
    </Card>
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
    <Card>
      <CardHeader>
        <CardTitle className="text-sm">Claude Code credentials</CardTitle>
      </CardHeader>
      <CardContent className="space-y-3 text-[13px]">
        <p className="text-[11px] text-[var(--text-tertiary)]">
          The agent runs as <em>you</em> using your Claude Code subscription. Paste your{" "}
          <code className="font-mono">~/.claude/.credentials.json</code> (the{" "}
          <code className="font-mono">claudeAiOauth</code> blob), or run{" "}
          <code className="font-mono">kuso incident-agent set-credentials</code> to upload it from
          your Keychain. Write-only — never shown back.
        </p>
        <StatusLine
          ok={status.ccConfigured}
          okText={`configured${status.ccSubscriptionType ? ` · ${status.ccSubscriptionType} sub` : ""}${
            status.ccExpiresAt ? ` · expires ${relativeTime(status.ccExpiresAt)}` : ""
          }`}
          badText="not configured — the agent can't authenticate"
        />
        {!readOnly && (
          <>
            <textarea
              value={creds}
              onChange={(e) => setCreds(e.target.value)}
              rows={4}
              spellCheck={false}
              placeholder='{"claudeAiOauth":{"accessToken":"...","refreshToken":"...","expiresAt":...,"subscriptionType":"max"}}'
              className="w-full resize-y rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-2 font-mono text-[11px] outline-none focus:border-[var(--border-strong)]"
            />
            <div className="flex justify-end">
              <Button size="sm" disabled={!creds.trim() || save.isPending} onClick={() => save.mutate()}>
                {save.isPending ? "Saving…" : "Save credentials"}
              </Button>
            </div>
          </>
        )}
      </CardContent>
    </Card>
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
        channelId: channelId !== status.channelId ? channelId : undefined,
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
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-sm">
          <MessageSquare className="h-4 w-4" /> Discord bridge
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3 text-[13px]">
        <p className="text-[11px] text-[var(--text-tertiary)]">
          The bot posts findings to a channel and relays your replies/reactions (✅ = go) back to
          the agent. Tokens are write-only. The bot needs the MESSAGE CONTENT intent + Send
          Messages / Create Public Threads on the channel.
        </p>
        <StatusLine
          ok={status.discordConfigured && status.botReady}
          okText={`bot connected${status.channelId ? ` · channel ${status.channelId}` : ""}`}
          badText={
            !status.discordConfigured
              ? "not configured"
              : !status.botDeployed
                ? "configured, but the bot deployment isn't running (apply deploy/incident-bot.yaml)"
                : "configured, but the bot isn't connected yet"
          }
        />
        {!readOnly && (
          <div className="space-y-2">
            <Field label="Discord bot token" value={botToken} onChange={setBotToken} placeholder="(unchanged)" secret />
            <Field label="kuso bot token (admin API)" value={kusoBotToken} onChange={setKusoBotToken} placeholder="(unchanged)" secret />
            <Field label="Channel ID" value={channelId} onChange={setChannelId} placeholder="123456789012345678" />
            <div className="flex justify-end">
              <Button size="sm" disabled={!dirty || save.isPending} onClick={() => save.mutate()}>
                {save.isPending ? "Saving…" : "Save Discord config"}
              </Button>
            </div>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

// --- small shared bits ---
function Toggle({ label, checked, onChange }: { label: string; checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <label className="flex cursor-pointer items-center gap-2 text-[12px] text-[var(--text-secondary)]">
      <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} />
      {label}
    </label>
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
    <div>
      <label className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">{label}</label>
      <Input
        type="number"
        value={value}
        disabled={disabled}
        onChange={(e) => onChange(parseInt(e.target.value || "0", 10))}
        className="mt-1 text-xs"
      />
      <p className="mt-0.5 text-[10px] text-[var(--text-tertiary)]">{hint}</p>
    </div>
  );
}

function Field({
  label,
  value,
  onChange,
  placeholder,
  secret,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  placeholder?: string;
  secret?: boolean;
}) {
  return (
    <div>
      <label className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">{label}</label>
      <Input
        type={secret ? "password" : "text"}
        value={value}
        placeholder={placeholder}
        onChange={(e) => onChange(e.target.value)}
        className="mt-1 font-mono text-xs"
      />
    </div>
  );
}

function StatusLine({ ok, okText, badText }: { ok: boolean; okText: string; badText: string }) {
  return (
    <div className={`flex items-center gap-1.5 text-[11px] ${ok ? "text-emerald-400" : "text-amber-400"}`}>
      <span>{ok ? "✓" : "⚠"}</span>
      <span>{ok ? okText : badText}</span>
    </div>
  );
}
