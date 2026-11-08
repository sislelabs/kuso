"use client";

import { Switch } from "@/components/ui/switch";
import { QueryErrorState } from "@/components/shared/QueryErrorState";
import { useEffect, useState } from "react";
import Link from "next/link";
import {
  useMutation,
  useQuery,
  useQueryClient,
  type UseQueryResult,
} from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { SaveButton } from "@/components/shared/SaveButton";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "sonner";
import { toastError } from "@/lib/errors";
import {
  Bell,
  BellRing,
  Check,
  ChevronDown,
  Mail,
  MessageSquare,
  Plus,
  Trash2,
  Send,
  Webhook,
  X,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { EmptyState } from "@/components/shared/EmptyState";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import {
  useNotificationEventTypes,
  type NotificationEventGroup,
  type NotificationEventType,
} from "@/features/notifications";
import { cleanMentionRules } from "./mentions";
import { useProjects } from "@/features/projects";
import { SettingsPage } from "@/components/settings/SettingsPage";
import { SettingsField, SettingsNotice, SettingsSection } from "@/components/settings/SettingsSection";
import { setBellPrefs, useBellPrefs } from "@/lib/notification-attention";
import { UserBadge } from "@/components/activity/UserBadge";
import { relativeTime } from "@/lib/format";

type NotifKind =
  | "discord"
  | "webhook"
  | "slack"
  | "mattermost"
  | "telegram"
  | "pushover"
  | "email";

// NOTIF_KINDS drives the type picker. Each entry lists the config
// keys its form collects; the editor renders one labelled input per
// key. urlKind entries also get the SSRF-guarded URL validation
// server-side.
const NOTIF_KINDS: {
  kind: NotifKind;
  label: string;
  fields: { key: string; label: string; placeholder: string; hint?: string }[];
}[] = [
  {
    kind: "discord",
    label: "Discord",
    fields: [
      {
        key: "url",
        label: "Discord webhook URL",
        placeholder: "https://discord.com/api/webhooks/...",
        hint: "Server Settings → Integrations → Webhooks → New Webhook.",
      },
    ],
  },
  {
    kind: "slack",
    label: "Slack",
    fields: [
      {
        key: "url",
        label: "Slack incoming webhook URL",
        placeholder: "https://hooks.slack.com/services/...",
        hint: "Slack app → Incoming Webhooks → Add New Webhook.",
      },
    ],
  },
  {
    kind: "mattermost",
    label: "Mattermost",
    fields: [
      {
        key: "url",
        label: "Mattermost incoming webhook URL",
        placeholder: "https://mattermost.example.com/hooks/...",
        hint: "Integrations → Incoming Webhooks.",
      },
    ],
  },
  {
    kind: "webhook",
    label: "Webhook",
    fields: [
      {
        key: "url",
        label: "Webhook URL",
        placeholder: "https://example.com/hooks/kuso",
        hint: "Any URL that accepts a JSON POST.",
      },
      {
        key: "secret",
        label: "Signing secret",
        placeholder: "Optional",
        hint: "When set, each delivery is signed with HMAC-SHA256.",
      },
    ],
  },
  {
    kind: "telegram",
    label: "Telegram",
    fields: [
      {
        key: "botToken",
        label: "Bot token",
        placeholder: "123456:ABC-DEF...",
        hint: "From @BotFather.",
      },
      {
        key: "chatId",
        label: "Chat ID",
        placeholder: "-1001234567890",
        hint: "The chat or channel to post to.",
      },
    ],
  },
  {
    kind: "pushover",
    label: "Pushover",
    fields: [
      {
        key: "token",
        label: "Application token",
        placeholder: "azGDORePK8gMaC0QOYAMyEEuzJnyUi",
      },
      {
        key: "user",
        label: "User or group key",
        placeholder: "uQiRzpo4DXghDmr9QzzfQu27cmVRsG",
      },
    ],
  },
  {
    kind: "email",
    label: "Email",
    fields: [
      { key: "host", label: "SMTP host", placeholder: "smtp.example.com" },
      { key: "port", label: "SMTP port", placeholder: "587", hint: "Defaults to 587." },
      { key: "username", label: "SMTP username", placeholder: "Optional" },
      { key: "password", label: "SMTP password", placeholder: "Optional" },
      { key: "from", label: "From address", placeholder: "kuso@example.com" },
      {
        key: "to",
        label: "To addresses",
        placeholder: "ops@example.com, oncall@example.com",
        hint: "Comma-separated.",
      },
    ],
  },
];

// kindFields returns the config field descriptors for a notif kind.
function kindFields(kind: NotifKind) {
  return NOTIF_KINDS.find((k) => k.kind === kind)?.fields ?? [];
}

interface Notification {
  id: string;
  name: string;
  type: NotifKind;
  enabled: boolean;
  pipelines: string[];
  events: string[];
  config: Record<string, unknown>;
  createdAt: string;
  updatedAt: string;
}

interface ListResp {
  data?: Notification[];
}

const EVENT_GROUPS: { group: NotificationEventGroup; label: string }[] = [
  { group: "build", label: "Build" },
  { group: "runtime", label: "Runtime" },
  { group: "jobs", label: "Jobs" },
  { group: "nodes", label: "Nodes" },
  { group: "backups", label: "Backups" },
  { group: "other", label: "Other" },
];

export default function NotificationsPage() {
  const list = useQuery({
    queryKey: ["admin", "notifications"],
    queryFn: async () => {
      const res = await api<ListResp | Notification[]>("/api/notifications");
      return Array.isArray(res) ? res : (res.data ?? []);
    },
  });
  const [editing, setEditing] = useState<string | null>(null);

  return (
    <SettingsPage
      title="Notifications"
      description={
        <>
          Channels for kuso events: Discord, Slack, Mattermost, Telegram, Pushover, email or a
          generic webhook. Channels are instance-wide; each gets the events and projects you pick.
          Rules for when to alert live in{" "}
          <Link href="/settings/alerts" className="text-[var(--text-primary)] underline underline-offset-2">
            Alert rules
          </Link>
          .
        </>
      }
      actions={
        <Button size="sm" onClick={() => setEditing("__new__")} disabled={editing === "__new__"}>
          <Plus className="h-3.5 w-3.5" />
          New channel
        </Button>
      }
    >
      <OutboxHealth />

      {editing === "__new__" && (
        <SettingsSection title="New channel" description="Pick a destination, then choose which projects and events reach it.">
          <NotificationEditor notification={null} onClose={() => setEditing(null)} />
        </SettingsSection>
      )}

      {list.isPending ? (
        <Skeleton className="h-32 w-full rounded-md" />
      ) : list.isError ? (
        <QueryErrorState what="notification channels" error={list.error} onRetry={() => void list.refetch()} />
      ) : (list.data ?? []).length === 0 ? (
        editing !== "__new__" && (
          <EmptyState
            icon={<Bell className="h-5 w-5" />}
            title="No notification channels yet"
            description="Click New channel to send kuso events to Discord, Slack, email or another destination."
            className="p-8"
          />
        )
      ) : (
        <SettingsSection
          title="Channels"
          description={`${(list.data ?? []).length} ${(list.data ?? []).length === 1 ? "channel" : "channels"}. Toggle to pause delivery without losing the config.`}
          flush
        >
          <ul className="divide-y divide-[var(--border-subtle)]">
            {(list.data ?? []).map((n) => (
              <li key={n.id}>
                <NotificationRow
                  n={n}
                  isEditing={editing === n.id}
                  onEdit={() => setEditing(n.id)}
                  onClose={() => setEditing(null)}
                />
              </li>
            ))}
          </ul>
        </SettingsSection>
      )}

      <MutedProjects />

      <BellPreferences />
    </SettingsPage>
  );
}

// BellPreferences — per-browser choices for the in-app bell. Stored
// locally (lib/notification-attention) since they only change what this
// person sees, not what any channel delivers.
function BellPreferences() {
  const prefs = useBellPrefs();
  return (
    <SettingsSection
      title="In-app bell"
      description="The bell shows what needs attention by default: failures, crashes, alerts, security findings and updates."
    >
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0">
          <p id="bell-count-all" className="text-sm font-medium text-[var(--text-primary)]">
            Count successes in the badge
          </p>
          <p id="bell-count-all-hint" className="mt-0.5 text-xs text-[var(--text-secondary)]">
            Off: successful builds, deploys and recoveries appear under All activity but don&apos;t light up the
            badge. Applies to this browser.
          </p>
        </div>
        <Switch
          on={prefs.countAll}
          onChange={(next) => setBellPrefs({ countAll: next })}
          labelledBy="bell-count-all"
          describedBy="bell-count-all-hint"
        />
      </div>
    </SettingsSection>
  );
}

// MutedProjects — roster of projects whose external delivery is muted
// (per-project toggle lives on each project's settings page). Rendered
// only when at least one mute exists, so the common all-unmuted case
// adds no visual noise.
function MutedProjects() {
  const qc = useQueryClient();
  const mutes = useQuery({
    queryKey: ["admin", "notifications", "muted-projects"],
    queryFn: () =>
      api<{ project: string; createdAt: string; createdBy?: string }[]>(
        "/api/notifications/muted-projects"
      ),
  });
  if (mutes.isPending || !mutes.data || mutes.data.length === 0) return null;
  return (
    <SettingsSection
      title="Muted projects"
      description="These projects' events skip every channel above but still land in the bell feed. Unmute here or from the project's settings page."
      flush
    >
      <ul className="divide-y divide-[var(--border-subtle)]">
        {mutes.data.map((m) => (
          <li
            key={m.project}
            className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 px-4 py-3 sm:px-5"
          >
            <div className="min-w-0">
              <Link
                href={`/projects/${encodeURIComponent(m.project)}`}
                className="font-mono text-sm text-[var(--text-primary)] hover:underline"
              >
                {m.project}
              </Link>
              <div className="mt-0.5 flex flex-wrap items-center gap-x-1.5 text-xs text-[var(--text-secondary)]">
                {m.createdBy ? (
                  <>
                    Muted by <UserBadge id={m.createdBy} className="text-xs" /> ·
                  </>
                ) : (
                  "Muted"
                )}
                <span title={new Date(m.createdAt).toLocaleString()}>{relativeTime(m.createdAt)}</span>
              </div>
            </div>
            <Button
              size="sm"
              variant="outline"
              onClick={async () => {
                try {
                  await api(
                    `/api/projects/${encodeURIComponent(m.project)}/notifications/mute`,
                    { method: "DELETE" }
                  );
                  toast.success(`Unmuted ${m.project}`);
                  void qc.invalidateQueries({
                    queryKey: ["admin", "notifications", "muted-projects"],
                  });
                } catch (e) {
                  toastError(e, "Failed to unmute");
                }
              }}
            >
              Unmute
            </Button>
          </li>
        ))}
      </ul>
    </SettingsSection>
  );
}

// OutboxHealth banner — pings GET /api/notifications/outbox-stats and
// renders an amber pending-backlog banner or a red dead-letter banner
// when webhook delivery is stuck. The bell-icon feed bypasses the
// outbox, so a red badge here only points to external-webhook
// misconfiguration (bad URL, revoked token), not in-app event flow.
function OutboxHealth() {
  const stats = useQuery({
    queryKey: ["admin", "notifications", "outbox-stats"],
    queryFn: () => api<{ pending: number; dead: number }>("/api/notifications/outbox-stats"),
    refetchInterval: 30_000,
  });
  if (stats.isPending || !stats.data) return null;
  const { pending, dead } = stats.data;
  if (dead === 0 && pending === 0) return null;

  if (dead > 0) {
    return (
      <SettingsNotice
        tone="error"
        title={`${dead} webhook ${dead === 1 ? "delivery" : "deliveries"} dead-lettered`}
      >
        Past the retry cap, typically a revoked Slack or Discord URL.
        {pending > 0 && ` ${pending} more pending retry.`} Inspect with{" "}
        <code className="break-all rounded-sm bg-[var(--bg-tertiary)] px-1 font-mono text-xs">
          SELECT * FROM &quot;NotificationOutbox&quot; WHERE &quot;deliveredAt&quot; IS NULL AND
          &quot;attempts&quot; &gt;= 10
        </code>
        .
      </SettingsNotice>
    );
  }
  return (
    <SettingsNotice
      tone="warning"
      title={`${pending} webhook ${pending === 1 ? "delivery" : "deliveries"} pending retry`}
    >
      Usually transient: Slack throttling or a brief webhook outage clears within a few minutes.
    </SettingsNotice>
  );
}

function NotificationRow({
  n,
  isEditing,
  onEdit,
  onClose,
}: {
  n: Notification;
  isEditing: boolean;
  onEdit: () => void;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const test = useMutation({
    mutationFn: () =>
      api(`/api/notifications/${encodeURIComponent(n.id)}/test`, { method: "POST" }),
    onSuccess: () => toast.success(`Test sent to ${n.name}`),
    onError: (e) => toastError(e, "Test failed"),
  });
  const [confirmOpen, setConfirmOpen] = useState(false);
  const remove = useMutation({
    mutationFn: () =>
      api(`/api/notifications/${encodeURIComponent(n.id)}`, { method: "DELETE" }),
    onSuccess: () => {
      toast.success(`${n.name} deleted`);
      qc.invalidateQueries({ queryKey: ["admin", "notifications"] });
      setConfirmOpen(false);
    },
    onError: (e) => toastError(e, "Delete failed"),
  });
  const toggle = useMutation({
    mutationFn: (enabled: boolean) =>
      api(`/api/notifications/${encodeURIComponent(n.id)}`, {
        method: "PUT",
        body: { ...n, enabled },
      }),
    onSuccess: (_d, enabled) => {
      toast.success(enabled ? `${n.name} enabled` : `${n.name} disabled`);
      qc.invalidateQueries({ queryKey: ["admin", "notifications"] });
    },
    onError: (e) => toastError(e, "Update failed"),
  });

  return (
    <div>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2 px-4 py-3 sm:flex-nowrap sm:px-5">
        <KindIcon kind={n.type} />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2 text-sm font-medium text-[var(--text-primary)]">
            <span className="truncate">{n.name}</span>
            {!n.enabled && (
              <span className="shrink-0 rounded-sm bg-[var(--bg-tertiary)] px-1.5 py-0.5 text-xs font-normal text-[var(--text-secondary)]">
                Paused
              </span>
            )}
          </div>
          <div className="mt-0.5 text-xs text-[var(--text-secondary)]">
            {NOTIF_KINDS.find((k) => k.kind === n.type)?.label ?? n.type} ·{" "}
            {n.events.length === 0 ? "all events" : `${n.events.length} events`} ·{" "}
            {(n.pipelines ?? []).length === 0
              ? "all projects"
              : `${n.pipelines.length} project${n.pipelines.length === 1 ? "" : "s"}`}
          </div>
        </div>
        <div className="flex w-full items-center justify-end gap-2 sm:w-auto">
        <Switch
          on={n.enabled}
          onChange={(v) => toggle.mutate(v)}
          disabled={toggle.isPending}
          label={n.enabled ? `Disable ${n.name}` : `Enable ${n.name}`}
        />
        <Button
          variant="outline"
          size="sm"
          onClick={() => test.mutate()}
          disabled={test.isPending || !n.enabled}
        >
          <Send className="h-3 w-3" />
          {test.isPending ? "Sending…" : "Test"}
        </Button>
        <Button variant="ghost" size="sm" onClick={isEditing ? onClose : onEdit}>
          {isEditing ? "Close" : "Edit"}
        </Button>
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label={`Delete ${n.name}`}
          onClick={() => setConfirmOpen(true)}
        >
          <Trash2 className="h-3.5 w-3.5" />
        </Button>
        </div>
      </div>
      {isEditing && (
        <div className="border-t border-[var(--border-subtle)] bg-[var(--bg-primary)]/40 p-4 sm:p-5">
          <NotificationEditor notification={n} onClose={onClose} />
        </div>
      )}
      <ConfirmDialog
        open={confirmOpen}
        title={`Delete ${n.name}?`}
        body={
          <p>
            Removes this {n.type} channel. Future events will no longer be
            sent here. The persisted notification feed is unaffected.
          </p>
        }
        confirmLabel="Delete channel"
        typeToConfirm={n.name}
        destructive
        pending={remove.isPending}
        onConfirm={() => remove.mutate()}
        onCancel={() => setConfirmOpen(false)}
      />
    </div>
  );
}

function KindIcon({ kind }: { kind: NotifKind }) {
  if (kind === "discord") {
    return (
      <svg viewBox="0 0 24 24" className="h-5 w-5 shrink-0" style={{ color: "#5865F2" }} aria-hidden>
        <path
          fill="currentColor"
          d="M20.317 4.37a19.791 19.791 0 0 0-4.885-1.515.074.074 0 0 0-.079.037c-.21.375-.444.864-.608 1.25a18.27 18.27 0 0 0-5.487 0 12.64 12.64 0 0 0-.617-1.25.077.077 0 0 0-.079-.037A19.736 19.736 0 0 0 3.677 4.37a.07.07 0 0 0-.032.027C.533 9.046-.32 13.58.099 18.057a.082.082 0 0 0 .031.057 19.9 19.9 0 0 0 5.993 3.03.078.078 0 0 0 .084-.028 14.09 14.09 0 0 0 1.226-1.994.076.076 0 0 0-.041-.106 13.107 13.107 0 0 1-1.872-.892.077.077 0 0 1-.008-.128 10.2 10.2 0 0 0 .372-.292.074.074 0 0 1 .077-.01c3.928 1.793 8.18 1.793 12.062 0a.074.074 0 0 1 .078.01c.12.098.246.198.373.292a.077.077 0 0 1-.006.127 12.299 12.299 0 0 1-1.873.892.077.077 0 0 0-.041.107c.36.698.772 1.362 1.225 1.993a.076.076 0 0 0 .084.028 19.839 19.839 0 0 0 6.002-3.03.077.077 0 0 0 .032-.054c.5-5.177-.838-9.674-3.549-13.66a.061.061 0 0 0-.031-.03zM8.02 15.33c-1.183 0-2.157-1.085-2.157-2.419 0-1.333.956-2.419 2.157-2.419 1.21 0 2.176 1.096 2.157 2.42 0 1.333-.956 2.418-2.157 2.418zm7.975 0c-1.183 0-2.157-1.085-2.157-2.419 0-1.333.955-2.419 2.157-2.419 1.21 0 2.176 1.096 2.157 2.42 0 1.333-.946 2.418-2.157 2.418z"
        />
      </svg>
    );
  }
  if (kind === "slack") {
    return (
      <svg viewBox="0 0 24 24" className="h-5 w-5 shrink-0" style={{ color: "#E01E5A" }} aria-hidden>
        <path
          fill="currentColor"
          d="M5.042 15.165a2.528 2.528 0 0 1-2.52 2.523A2.528 2.528 0 0 1 0 15.165a2.527 2.527 0 0 1 2.522-2.52h2.52v2.52zM6.313 15.165a2.527 2.527 0 0 1 2.521-2.52 2.527 2.527 0 0 1 2.521 2.52v6.313A2.528 2.528 0 0 1 8.834 24a2.528 2.528 0 0 1-2.521-2.522v-6.313zM8.834 5.042a2.528 2.528 0 0 1-2.521-2.52A2.528 2.528 0 0 1 8.834 0a2.528 2.528 0 0 1 2.521 2.522v2.52H8.834zM8.834 6.313a2.528 2.528 0 0 1 2.521 2.521 2.528 2.528 0 0 1-2.521 2.521H2.522A2.528 2.528 0 0 1 0 8.834a2.528 2.528 0 0 1 2.522-2.521h6.312zM18.956 8.834a2.528 2.528 0 0 1 2.522-2.521A2.528 2.528 0 0 1 24 8.834a2.528 2.528 0 0 1-2.522 2.521h-2.522V8.834zM17.688 8.834a2.528 2.528 0 0 1-2.523 2.521 2.527 2.527 0 0 1-2.52-2.521V2.522A2.527 2.527 0 0 1 15.165 0a2.528 2.528 0 0 1 2.523 2.522v6.312zM15.165 18.956a2.528 2.528 0 0 1 2.523 2.522A2.528 2.528 0 0 1 15.165 24a2.527 2.527 0 0 1-2.52-2.522v-2.522h2.52zM15.165 17.688a2.527 2.527 0 0 1-2.52-2.523 2.526 2.526 0 0 1 2.52-2.52h6.313A2.527 2.527 0 0 1 24 15.165a2.528 2.528 0 0 1-2.522 2.523h-6.313z"
        />
      </svg>
    );
  }
  if (kind === "mattermost") {
    return <MessageSquare className="h-5 w-5 shrink-0" style={{ color: "#1E325C" }} aria-hidden />;
  }
  if (kind === "telegram") {
    return <Send className="h-5 w-5 shrink-0" style={{ color: "#229ED9" }} aria-hidden />;
  }
  if (kind === "pushover") {
    return <BellRing className="h-5 w-5 shrink-0" style={{ color: "#249DF1" }} aria-hidden />;
  }
  if (kind === "email") {
    return <Mail className="h-5 w-5 shrink-0 text-[var(--text-tertiary)]" aria-hidden />;
  }
  return <Webhook className="h-5 w-5 shrink-0 text-[var(--text-tertiary)]" />;
}

function editorSnapshot(n: Notification) {
  const cfg: Record<string, string> = {};
  for (const [k, v] of Object.entries(n.config ?? {})) {
    if (typeof v === "string" && v !== "") cfg[k] = v;
    else if (typeof v === "number") cfg[k] = String(v);
  }
  return {
    name: n.name,
    type: n.type,
    enabled: n.enabled,
    cfg,
    events: n.events ?? [],
    pipelines: n.pipelines ?? [],
    mentions: (n.config.mentions as Record<string, string> | undefined) ?? {},
  };
}

function NotificationEditor({
  notification,
  onClose,
}: {
  notification: Notification | null;
  onClose: () => void;
}) {
  const isNew = !notification;
  const qc = useQueryClient();
  const [name, setName] = useState(notification?.name ?? "");
  const [type, setType] = useState<NotifKind>(notification?.type ?? "discord");
  const [enabled, setEnabled] = useState(notification?.enabled ?? true);
  // cfg holds every per-type config key (url / botToken / host / ...).
  // Keys not relevant to the current type are simply ignored on save.
  const [cfg, setCfg] = useState<Record<string, string>>(() => {
    const c: Record<string, string> = {};
    for (const [k, v] of Object.entries(notification?.config ?? {})) {
      if (typeof v === "string") c[k] = v;
      else if (typeof v === "number") c[k] = String(v);
    }
    return c;
  });
  // Empty events = every event type, including ones added in later
  // releases. eventsMode makes that explicit: "custom" with nothing
  // ticked would silently mean "all" on the server, so save is blocked
  // in that state instead.
  const [events, setEvents] = useState<string[]>(notification?.events ?? []);
  const [eventsMode, setEventsMode] = useState<"all" | "custom">(
    (notification?.events ?? []).length === 0 ? "all" : "custom"
  );
  // pipelines is the per-channel project whitelist; empty = all projects.
  // Carried through on save so a filter set via the CLI isn't wiped.
  const [pipelines, setPipelines] = useState<string[]>(notification?.pipelines ?? []);
  const [projectsMode, setProjectsMode] = useState<"all" | "selected">(
    (notification?.pipelines ?? []).length === 0 ? "all" : "selected"
  );
  const eventTypes = useNotificationEventTypes();
  // Per-event mention rules. Special key "*" applies to events
  // without an explicit entry. Server-side default (when nothing
  // here matches) is @here for error-severity events, none
  // otherwise — see notify.mentionFor.
  const [mentions, setMentions] = useState<Record<string, string>>(
    (notification?.config.mentions as Record<string, string> | undefined) ?? {}
  );

  useEffect(() => {
    if (!notification) return;
    setName(notification.name);
    setType(notification.type);
    setEnabled(notification.enabled);
    const c: Record<string, string> = {};
    for (const [k, v] of Object.entries(notification.config ?? {})) {
      if (typeof v === "string") c[k] = v;
      else if (typeof v === "number") c[k] = String(v);
    }
    setCfg(c);
    setEvents(notification.events);
    setEventsMode(notification.events.length === 0 ? "all" : "custom");
    setPipelines(notification.pipelines ?? []);
    setProjectsMode((notification.pipelines ?? []).length === 0 ? "all" : "selected");
    setMentions((notification.config.mentions as Record<string, string> | undefined) ?? {});
  }, [notification]);

  // optionalConfigKeys are config fields the save button doesn't
  // require — email's auth + port have sane defaults / can be empty.
  const optionalConfigKeys = new Set(["port", "username", "password", "secret"]);
  const configComplete = kindFields(type).every(
    (f) => optionalConfigKeys.has(f.key) || (cfg[f.key] ?? "").trim() !== "",
  );
  const eventsIncomplete = eventsMode === "custom" && events.length === 0;
  const projectsIncomplete = projectsMode === "selected" && pipelines.length === 0;
  // Edits compare against the stored channel; a new channel is always
  // savable once complete.
  const dirty =
    isNew ||
    JSON.stringify(editorSnapshot(notification)) !==
      JSON.stringify({
        name,
        type,
        enabled,
        cfg: Object.fromEntries(Object.entries(cfg).filter(([, v]) => v !== "")),
        events: eventsMode === "all" ? [] : events,
        pipelines: projectsMode === "all" ? [] : pipelines,
        mentions,
      });

  const save = useMutation({
    mutationFn: () => {
      // "none" is an explicit opt-out and must survive when the
      // fallback would ping; see cleanMentionRules.
      const cleanMentions = cleanMentionRules(mentions, eventTypes.data);
      // Only persist the config keys this channel type actually uses,
      // so switching type doesn't leave stale keys (a leftover `url`
      // on a telegram channel, etc.) in the stored config.
      // An update with the type unchanged is MERGED into the stored config
      // server-side: omitted keys are kept (so keys set via CLI/API that the
      // form doesn't render survive) and only a null deletes. So a field
      // the user cleared, or removed mention overrides, must be sent as
      // null. A create or a type change replaces the config, so no nulls.
      const merging = !!notification && !isNew && notification.type === type;
      const typeConfig: Record<string, unknown> = {};
      for (const f of kindFields(type)) {
        if (cfg[f.key]) typeConfig[f.key] = cfg[f.key];
        else if (merging) typeConfig[f.key] = null;
      }
      // mentions only mean anything for discord, but harmless to carry;
      // the server ignores them on other channel types.
      if (Object.keys(cleanMentions).length) typeConfig.mentions = cleanMentions;
      else if (merging) typeConfig.mentions = null;
      const body = {
        name,
        type,
        enabled,
        pipelines: projectsMode === "all" ? [] : pipelines,
        events: eventsMode === "all" ? [] : events,
        config: typeConfig,
      };
      if (isNew) {
        return api("/api/notifications", { method: "POST", body });
      }
      return api(`/api/notifications/${encodeURIComponent(notification!.id)}`, {
        method: "PUT",
        body: { ...body, id: notification!.id },
      });
    },
    onSuccess: () => {
      toast.success(isNew ? "Channel created" : "Channel saved");
      qc.invalidateQueries({ queryKey: ["admin", "notifications"] });
      onClose();
    },
    onError: (e) => toastError(e, "Save failed"),
  });

  return (
    <div>
      <div className="space-y-5">
        <SettingsField label="Name" hint="Only shown to you, in this list and in test messages.">
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="e.g. team-discord"
            autoFocus={isNew}
          />
        </SettingsField>
        <SettingsField label="Destination">
          <div className="inline-flex flex-wrap gap-1 rounded-sm border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-0.5">
            {NOTIF_KINDS.map(({ kind: k, label }) => (
              <button
                key={k}
                type="button"
                onClick={() => setType(k)}
                className={cn(
                  "inline-flex h-8 items-center gap-1.5 rounded-sm px-2.5 text-sm transition-colors [&_svg]:h-4 [&_svg]:w-4",
                  type === k
                    ? "bg-[var(--bg-tertiary)] text-[var(--text-primary)]"
                    : "text-[var(--text-tertiary)] hover:text-[var(--text-primary)]"
                )}
              >
                <KindIcon kind={k} />
                {label}
              </button>
            ))}
          </div>
        </SettingsField>
        {kindFields(type).map((f) => (
          <SettingsField key={f.key} label={f.label} hint={f.hint}>
            <Input
              value={cfg[f.key] ?? ""}
              onChange={(e) => setCfg((c) => ({ ...c, [f.key]: e.target.value }))}
              placeholder={f.placeholder}
              type={f.key === "password" ? "password" : "text"}
              className="font-mono"
              spellCheck={false}
            />
          </SettingsField>
        ))}
        <SettingsField label="Projects" hint="Which projects' events reach this channel.">
          <ProjectFilter
            mode={projectsMode}
            onModeChange={setProjectsMode}
            selected={pipelines}
            onChange={setPipelines}
          />
        </SettingsField>
        <SettingsField
          label="Events"
          hint={
            type === "discord"
              ? "All events, or a custom subset. Set per-event Discord mentions on each row."
              : "All events, or a custom subset."
          }
        >
          <EventPicker
            eventTypes={eventTypes}
            mode={eventsMode}
            onModeChange={setEventsMode}
            events={events}
            onEventsChange={setEvents}
            mentions={type === "discord" ? mentions : null}
            onMentionChange={(id, v) => setMentions((cur) => ({ ...cur, [id]: v }))}
          />
        </SettingsField>
        <SettingsField
          label="Enabled"
          hint="Paused channels keep their config but receive nothing."
          aside={
          <Switch on={enabled} onChange={setEnabled} label="Enabled" />
          }
        />
      </div>
      <footer className="mt-5 flex items-center justify-end gap-2 border-t border-[var(--border-subtle)] pt-4">
        <Button variant="ghost" size="sm" onClick={onClose} disabled={save.isPending}>
          Cancel
        </Button>
        <SaveButton
          dirty={dirty}
          saving={save.isPending}
          onClick={() => save.mutate()}
          label={isNew ? "Create" : "Save"}
          blockedReason={
            !name
              ? "Give the channel a name"
              : !configComplete
                ? "Fill in the required connection fields"
                : eventsIncomplete
                  ? "Pick at least one event, or send all events"
                  : projectsIncomplete
                    ? "Pick at least one project, or send for all projects"
                    : undefined
          }
        />
      </footer>
    </div>
  );
}

function SegmentedToggle<T extends string>({
  options,
  value,
  onChange,
}: {
  options: { v: T; label: string }[];
  value: T;
  onChange: (v: T) => void;
}) {
  return (
    <div className="inline-flex gap-1 rounded-sm border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-0.5">
      {options.map((o) => (
        <button
          key={o.v}
          type="button"
          onClick={() => onChange(o.v)}
          aria-pressed={value === o.v}
          className={cn(
            "inline-flex h-8 items-center rounded-sm px-2.5 text-sm transition-colors",
            value === o.v
              ? "bg-[var(--bg-tertiary)] text-[var(--text-primary)]"
              : "text-[var(--text-tertiary)] hover:text-[var(--text-primary)]"
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

// ProjectFilter edits the channel's project whitelist (the `pipelines`
// field on the wire). "All projects" persists an empty list.
function ProjectFilter({
  mode,
  onModeChange,
  selected,
  onChange,
}: {
  mode: "all" | "selected";
  onModeChange: (m: "all" | "selected") => void;
  selected: string[];
  onChange: (next: string[]) => void;
}) {
  const projects = useProjects();
  const existing = new Set((projects.data ?? []).map((p) => p.metadata.name));
  // Union with the stored selection so a filter naming a since-deleted
  // project stays visible (and removable) instead of vanishing.
  const names = Array.from(new Set([...existing, ...selected])).sort();
  const toggle = (name: string) =>
    onChange(selected.includes(name) ? selected.filter((x) => x !== name) : [...selected, name]);

  return (
    <div className="space-y-2">
      <SegmentedToggle
        options={[
          { v: "all", label: "All projects" },
          { v: "selected", label: "Selected projects" },
        ]}
        value={mode}
        onChange={onModeChange}
      />
      {mode === "selected" && (
        <div className="flex flex-wrap items-center gap-1">
          {selected.map((name) => (
            <span
              key={name}
              className="inline-flex h-7 items-center gap-1 rounded-sm border border-[var(--border-subtle)] bg-[var(--bg-secondary)] pl-2 pr-0.5 font-mono text-xs"
            >
              {name}
              {projects.isSuccess && !existing.has(name) && (
                <span className="text-[var(--text-tertiary)]">(missing)</span>
              )}
              <button
                type="button"
                onClick={() => toggle(name)}
                aria-label={`Remove ${name}`}
                className="inline-flex h-4 w-4 items-center justify-center rounded text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
              >
                <X className="h-3 w-3" />
              </button>
            </span>
          ))}
          <Popover>
            <PopoverTrigger
              className={cn(
                "inline-flex h-7 items-center gap-1 rounded-sm border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 text-xs text-[var(--text-secondary)]",
                "hover:border-[var(--border-strong)] data-[popup-open]:border-[var(--border-strong)]"
              )}
            >
              <Plus className="h-3 w-3" />
              {selected.length === 0 ? "Choose projects" : "Edit"}
              <ChevronDown className="h-3 w-3 text-[var(--text-tertiary)]" />
            </PopoverTrigger>
            <PopoverContent
              align="start"
              sideOffset={4}
              className="max-h-72 w-56 gap-0.5 overflow-y-auto rounded-md p-1"
            >
              {projects.isPending ? (
                <div className="px-1.5 py-1 text-xs text-[var(--text-secondary)]">
                  loading projects…
                </div>
              ) : projects.isError ? (
                <div className="px-1.5 py-1 text-xs text-[var(--error)]">
                  couldn&rsquo;t load projects
                </div>
              ) : names.length === 0 ? (
                <div className="px-1.5 py-1 text-xs text-[var(--text-secondary)]">
                  no projects
                </div>
              ) : (
                names.map((name) => {
                  const on = selected.includes(name);
                  return (
                    <button
                      key={name}
                      type="button"
                      onClick={() => toggle(name)}
                      className={cn(
                        "flex w-full items-center gap-1.5 rounded px-1.5 py-1 text-left font-mono text-xs",
                        "hover:bg-[var(--bg-tertiary)]",
                        on ? "text-[var(--text-primary)]" : "text-[var(--text-secondary)]"
                      )}
                    >
                      <Check className={cn("h-3 w-3 shrink-0", on ? "opacity-100" : "opacity-0")} />
                      <span className="truncate">{name}</span>
                    </button>
                  );
                })
              )}
            </PopoverContent>
          </Popover>
        </div>
      )}
      <p className="text-xs text-[var(--text-secondary)]">
        {mode === "all"
          ? "Events from every project, including projects created later."
          : selected.length === 0
            ? "Pick at least one project, or switch to All projects."
            : `Only events from ${selected.length === 1 ? "this project" : `these ${selected.length} projects`}.`}
      </p>
    </div>
  );
}

// EventPicker renders the channel's event subscription from the server's
// event catalogue. "All events" persists an empty list, which the server
// treats as every event type — including types added in later releases.
// A custom subset only ever receives what's ticked.
function EventPicker({
  eventTypes,
  mode,
  onModeChange,
  events,
  onEventsChange,
  mentions,
  onMentionChange,
}: {
  eventTypes: UseQueryResult<NotificationEventType[]>;
  mode: "all" | "custom";
  onModeChange: (m: "all" | "custom") => void;
  events: string[];
  onEventsChange: (next: string[]) => void;
  // null when the channel type has no mention support (non-discord).
  mentions: Record<string, string> | null;
  onMentionChange: (eventID: string, v: string) => void;
}) {
  const catalogue = eventTypes.data ?? [];
  const known = new Set(catalogue.map((t) => t.type));
  const knownGroups = new Set<string>(EVENT_GROUPS.map((g) => g.group));
  const groupOf = (t: NotificationEventType): NotificationEventGroup =>
    knownGroups.has(t.group) ? t.group : "other";
  // Stored types the server no longer emits (e.g. deploy.rolled).
  const stale = eventTypes.isSuccess ? events.filter((e) => !known.has(e)) : [];
  const toggle = (id: string) =>
    onEventsChange(events.includes(id) ? events.filter((x) => x !== id) : [...events, id]);
  // With "all events" and no mention column there's nothing to edit
  // per row, so the list is skipped.
  const showRows = mode === "custom" || mentions !== null;

  return (
    <div className="space-y-2">
      <SegmentedToggle
        options={[
          { v: "all", label: "All events" },
          { v: "custom", label: "Custom" },
        ]}
        value={mode}
        onChange={onModeChange}
      />
      <p className="text-xs text-[var(--text-secondary)]">
        {mode === "all"
          ? "Every event type, including ones added in future kuso releases."
          : events.length === 0
            ? "Pick at least one event, or switch to All events."
            : `${events.length} event${events.length === 1 ? "" : "s"} selected. Event types added in later releases are not included until you tick them.`}
      </p>
      {showRows &&
        (eventTypes.isPending ? (
          <Skeleton className="h-24 w-full rounded-md" />
        ) : eventTypes.isError ? (
          <QueryErrorState
            what="event types"
            error={eventTypes.error}
            onRetry={() => void eventTypes.refetch()}
          />
        ) : (
          <div className="space-y-3">
            {EVENT_GROUPS.map(({ group, label }) => {
              const rows = catalogue.filter((t) => groupOf(t) === group);
              if (rows.length === 0) return null;
              return (
                <div key={group} className="space-y-1.5">
                  <div className="text-xs font-medium text-[var(--text-secondary)]">{label}</div>
                  {rows.map((t) => (
                    <EventRow
                      key={t.type}
                      id={t.type}
                      label={t.label}
                      picked={mode === "all" || events.includes(t.type)}
                      toggleable={mode === "custom"}
                      onToggle={() => toggle(t.type)}
                      mention={mentions === null ? null : (mentions[t.type] ?? "")}
                      defaultMention={t.defaultMention}
                      onMentionChange={(v) => onMentionChange(t.type, v)}
                    />
                  ))}
                </div>
              );
            })}
            {mode === "custom" && stale.length > 0 && (
              <div className="space-y-1.5">
                <div className="text-xs font-medium text-[var(--text-secondary)]">
                  Not emitted by this server
                </div>
                {stale.map((id) => (
                  <EventRow
                    key={id}
                    id={id}
                    label="never fires; untick to remove"
                    picked
                    toggleable
                    onToggle={() => toggle(id)}
                    mention={null}
                    defaultMention=""
                    onMentionChange={() => {}}
                  />
                ))}
              </div>
            )}
          </div>
        ))}
    </div>
  );
}

function EventRow({
  id,
  label,
  picked,
  toggleable,
  onToggle,
  mention,
  defaultMention,
  onMentionChange,
}: {
  id: string;
  label: string;
  picked: boolean;
  toggleable: boolean;
  onToggle: () => void;
  // null hides the mention picker column.
  mention: string | null;
  defaultMention: string;
  onMentionChange: (v: string) => void;
}) {
  return (
    <div
      className={cn(
        // Fixed-grid layout so the mention picker column stays in the
        // same x-axis spot whether or not the event is picked. On
        // phones the picker column wraps below the rest (the 180px
        // column would force a ~640px-wide row otherwise).
        "grid grid-cols-[44px_1fr] items-center gap-x-2 gap-y-1 rounded-sm border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-3 py-1.5 text-xs sm:grid-cols-[44px_180px_1fr_160px]"
      )}
    >
      <button
        type="button"
        onClick={onToggle}
        disabled={!toggleable}
        aria-label={picked ? `Disable ${id}` : `Enable ${id}`}
        className={cn(
          "inline-flex h-5 w-9 shrink-0 items-center rounded-full border transition-colors",
          picked
            ? "border-[var(--success)]/40 bg-[var(--success)]/25"
            : "border-[var(--border-subtle)] bg-[var(--bg-tertiary)]",
          !toggleable && "cursor-default opacity-50"
        )}
      >
        <span
          className={cn(
            "inline-block h-3.5 w-3.5 rounded-full bg-white shadow transition-transform",
            picked ? "translate-x-4" : "translate-x-0.5"
          )}
        />
      </button>
      <span
        className={cn(
          "truncate font-mono",
          picked ? "text-[var(--text-primary)]" : "text-[var(--text-tertiary)]"
        )}
      >
        {id}
      </span>
      <span
        className={cn(
          "col-start-2 truncate sm:col-start-auto",
          picked ? "text-[var(--text-secondary)]" : "text-[var(--text-tertiary)]"
        )}
      >
        {label}
      </span>
      {mention !== null ? (
        <div className="col-start-2 sm:col-start-auto">
        <MentionPicker
          value={mention}
          onChange={onMentionChange}
          defaultMention={defaultMention}
          disabled={!picked}
        />
        </div>
      ) : (
        <span />
      )}
    </div>
  );
}

// MentionPicker is the per-event Discord mention selector. Five
// options: server default (which is @here for outage events,
// nothing otherwise), explicit none, @here, @everyone, custom role.
//
// It's a Popover (not a native <select>) because a native select
// closes the moment you pick an option, and per-event tuning across a
// list of events reads better when the panel stays open until you
// click away — pick a rule, see it apply, move on or change your mind.
// Base-ui Popover closes on outside-click / Esc and does NOT close on
// an in-panel click, which is exactly the behaviour we want.
//
// The custom-role path needs a Discord role ID — which means right-
// clicking the role in Discord and picking "Copy Role ID" in dev
// mode. We don't try to fetch the guild's roles because the bot
// scope kuso uses (incoming-webhook) doesn't have permission.
function MentionPicker({
  value,
  onChange,
  defaultMention,
  disabled,
}: {
  value: string;
  onChange: (v: string) => void;
  defaultMention: string;
  disabled?: boolean;
}) {
  // value is one of: "" (use default), "none", "@here", "@everyone",
  // "role:<id>", or any other custom string.
  const isRole = value.startsWith("role:");
  const roleID = isRole ? value.slice("role:".length) : "";

  const options: { v: string; label: string }[] = [
    { v: "", label: defaultMention ? `default (${defaultMention})` : "default (none)" },
    { v: "none", label: "none" },
    { v: "@here", label: "@here" },
    { v: "@everyone", label: "@everyone" },
    { v: "role:", label: "role…" },
  ];

  // Label shown on the trigger button mirrors the old <select> display.
  const current = isRole ? "role…" : options.find((o) => o.v === value);
  const triggerLabel = isRole ? "role…" : (current as { label: string }).label;
  // Which option row is "selected" for the check mark.
  const selectedV = isRole ? "role:" : value;

  return (
    <Popover>
      <PopoverTrigger
        disabled={disabled}
        className={cn(
          "inline-flex h-7 w-full items-center justify-between gap-1 rounded-sm border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 font-mono text-xs text-[var(--text-primary)]",
          "hover:border-[var(--border-strong)] data-[popup-open]:border-[var(--border-strong)]",
          disabled && "pointer-events-none opacity-40"
        )}
      >
        <span className="truncate">{triggerLabel}</span>
        <ChevronDown className="h-3 w-3 shrink-0 text-[var(--text-tertiary)]" />
      </PopoverTrigger>
      <PopoverContent
        align="start"
        sideOffset={4}
        className="w-44 gap-0.5 rounded-md p-1"
      >
        {options.map((o) => (
          <button
            key={o.v}
            type="button"
            onClick={() => onChange(o.v === "role:" ? "role:" + roleID : o.v)}
            className={cn(
              "flex w-full items-center gap-1.5 rounded px-1.5 py-1 text-left font-mono text-xs",
              "hover:bg-[var(--bg-tertiary)]",
              o.v === selectedV ? "text-[var(--text-primary)]" : "text-[var(--text-secondary)]"
            )}
          >
            <Check
              className={cn(
                "h-3 w-3 shrink-0",
                o.v === selectedV ? "opacity-100" : "opacity-0"
              )}
            />
            <span className="truncate">{o.label}</span>
          </button>
        ))}
        {isRole && (
          <input
            value={roleID}
            onChange={(e) => onChange("role:" + e.target.value)}
            placeholder="role ID"
            className="mt-1 h-7 w-full rounded-sm border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 font-mono text-xs"
            spellCheck={false}
            // Keep clicks/keystrokes in the input from bubbling to the
            // option buttons; the popover itself stays open regardless.
            onClick={(e) => e.stopPropagation()}
          />
        )}
      </PopoverContent>
    </Popover>
  );
}
