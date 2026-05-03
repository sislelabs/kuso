"use client";

import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "sonner";
import { Bell, Plus, Trash2, Send, CheckCircle2, Webhook } from "lucide-react";
import { cn } from "@/lib/utils";

type NotifKind = "discord" | "webhook" | "slack";

interface Notification {
  id: string;
  name: string;
  type: NotifKind;
  enabled: boolean;
  pipelines: string[];
  events: string[];
  config: Record<string, string | undefined>;
  createdAt: string;
  updatedAt: string;
}

interface ListResp {
  data?: Notification[];
}

const ALL_EVENTS = [
  { id: "build.started",     label: "Build started" },
  { id: "build.succeeded",   label: "Build succeeded" },
  { id: "build.failed",      label: "Build failed" },
  { id: "deploy.rolled",     label: "Deploy rolled" },
  { id: "pod.crashed",       label: "Pod crashed" },
  { id: "alert.fired",       label: "Alert fired" },
  { id: "backup.succeeded",  label: "Backup succeeded" },
  { id: "backup.failed",     label: "Backup failed" },
] as const;

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
    <div className="mx-auto max-w-3xl p-6 lg:p-8">
      <header className="mb-6 flex items-center gap-3">
        <Bell className="h-5 w-5 text-[var(--text-tertiary)]" />
        <div>
          <h1 className="font-heading text-xl font-semibold tracking-tight">Notifications</h1>
          <p className="mt-0.5 text-xs text-[var(--text-secondary)]">
            Discord webhooks, generic webhook fan-out. Configure once instance-wide; every
            build/crash/alert event lands in every enabled channel.
          </p>
        </div>
      </header>

      <div className="mb-3 flex items-center justify-end">
        <Button size="sm" onClick={() => setEditing("__new__")}>
          <Plus className="h-3 w-3" />
          New channel
        </Button>
      </div>

      {list.isPending ? (
        <Skeleton className="h-32 w-full rounded-md" />
      ) : (list.data ?? []).length === 0 ? (
        <p className="rounded-md border border-dashed border-[var(--border-subtle)] p-8 text-center text-sm text-[var(--text-tertiary)]">
          No notification channels yet. Click <span className="font-mono">+ New channel</span>{" "}
          to add a Discord webhook.
        </p>
      ) : (
        <ul className="space-y-2">
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
      )}

      {editing === "__new__" && (
        <NotificationEditor notification={null} onClose={() => setEditing(null)} />
      )}
    </div>
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
    onError: (e) => toast.error(e instanceof Error ? e.message : "Test failed"),
  });
  const remove = useMutation({
    mutationFn: () =>
      api(`/api/notifications/${encodeURIComponent(n.id)}`, { method: "DELETE" }),
    onSuccess: () => {
      toast.success(`${n.name} deleted`);
      qc.invalidateQueries({ queryKey: ["admin", "notifications"] });
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Delete failed"),
  });
  const toggle = useMutation({
    mutationFn: (enabled: boolean) =>
      api(`/api/notifications/${encodeURIComponent(n.id)}`, {
        method: "PUT",
        body: { ...n, enabled },
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["admin", "notifications"] }),
  });

  return (
    <div className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
      <div className="flex items-center gap-3 px-4 py-3">
        <KindIcon kind={n.type} />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2 truncate text-sm font-medium">
            {n.name}
            {!n.enabled && (
              <span className="rounded bg-[var(--bg-tertiary)] px-1.5 py-0.5 font-mono text-[9px] uppercase tracking-widest text-[var(--text-tertiary)]">
                disabled
              </span>
            )}
          </div>
          <div className="mt-0.5 font-mono text-[10px] text-[var(--text-tertiary)]">
            {n.type} · {n.events.length === 0 ? "all events" : `${n.events.length} events`}
          </div>
        </div>
        <button
          type="button"
          onClick={() => toggle.mutate(!n.enabled)}
          className={cn(
            "inline-flex h-6 w-10 shrink-0 items-center rounded-full border transition-colors",
            n.enabled
              ? "border-emerald-500/30 bg-emerald-500/20"
              : "border-[var(--border-subtle)] bg-[var(--bg-tertiary)]"
          )}
          aria-label={n.enabled ? "Disable" : "Enable"}
        >
          <span
            className={cn(
              "inline-block h-4 w-4 rounded-full bg-white shadow transition-transform",
              n.enabled ? "translate-x-5" : "translate-x-1"
            )}
          />
        </button>
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
          aria-label="Delete"
          onClick={() => {
            if (window.confirm(`Delete ${n.name}?`)) remove.mutate();
          }}
        >
          <Trash2 className="h-3.5 w-3.5" />
        </Button>
      </div>
      {isEditing && <NotificationEditor notification={n} onClose={onClose} />}
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
  return <Webhook className="h-5 w-5 shrink-0 text-[var(--text-tertiary)]" />;
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
  const [url, setUrl] = useState<string>(
    (notification?.config.url as string | undefined) ?? ""
  );
  const [events, setEvents] = useState<string[]>(notification?.events ?? []);
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
    setUrl((notification.config.url as string | undefined) ?? "");
    setEvents(notification.events);
    setMentions((notification.config.mentions as Record<string, string> | undefined) ?? {});
  }, [notification]);

  const save = useMutation({
    mutationFn: () => {
      // Strip empty/none mention rules before save — they're the
      // implicit default and clutter the stored config otherwise.
      const cleanMentions: Record<string, string> = {};
      for (const [k, v] of Object.entries(mentions)) {
        if (v && v !== "none") cleanMentions[k] = v;
      }
      const body = {
        name,
        type,
        enabled,
        pipelines: [],
        events,
        config: {
          url,
          ...(Object.keys(cleanMentions).length ? { mentions: cleanMentions } : {}),
        },
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
    onError: (e) => toast.error(e instanceof Error ? e.message : "Save failed"),
  });

  return (
    <div
      className={cn(
        "border-[var(--border-subtle)] bg-[var(--bg-primary)] px-4 py-3",
        !isNew && "border-t",
        isNew && "mt-3 rounded-md border bg-[var(--bg-secondary)]"
      )}
    >
      <div className="space-y-3">
        <Field label="name" hint="memo">
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="kuso-team-discord"
            className="h-8 font-mono text-[12px]"
            autoFocus={isNew}
          />
        </Field>
        <Field label="type">
          <div className="inline-flex flex-wrap gap-1 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-0.5">
            {(["discord", "webhook"] as NotifKind[]).map((k) => (
              <button
                key={k}
                type="button"
                onClick={() => setType(k)}
                className={cn(
                  "inline-flex h-7 items-center gap-1.5 rounded px-2 font-mono text-[11px] transition-colors",
                  type === k
                    ? "bg-[var(--bg-tertiary)] text-[var(--text-primary)]"
                    : "text-[var(--text-tertiary)] hover:text-[var(--text-primary)]"
                )}
              >
                <KindIcon kind={k} />
                {k}
              </button>
            ))}
          </div>
        </Field>
        <Field
          label={type === "discord" ? "discord webhook url" : "webhook url"}
          hint={
            type === "discord"
              ? "Server Settings → Integrations → Webhooks → New Webhook"
              : "any URL that accepts a JSON POST"
          }
        >
          <Input
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            placeholder={
              type === "discord"
                ? "https://discord.com/api/webhooks/..."
                : "https://example.com/hooks/kuso"
            }
            className="h-8 font-mono text-[12px]"
            spellCheck={false}
          />
        </Field>
        <Field
          label="events"
          hint="toggle the events you want; configure per-event Discord mentions on the right"
        >
          <div className="space-y-1.5">
            {ALL_EVENTS.map((e) => {
              const isPicked = events.includes(e.id);
              return (
                <div
                  key={e.id}
                  className={cn(
                    // Fixed-grid layout so the mention picker column
                    // stays in the same x-axis spot whether or not
                    // the event is picked. Without min-width on the
                    // picker column the row would shrink when the
                    // toggle was off and the layout would jump.
                    "grid grid-cols-[44px_180px_1fr_180px] items-center gap-2 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-3 py-1.5 text-[11px]"
                  )}
                >
                  <button
                    type="button"
                    onClick={() =>
                      setEvents((cur) =>
                        isPicked ? cur.filter((x) => x !== e.id) : [...cur, e.id]
                      )
                    }
                    aria-label={isPicked ? `Disable ${e.id}` : `Enable ${e.id}`}
                    className={cn(
                      "inline-flex h-5 w-9 shrink-0 items-center rounded-full border transition-colors",
                      isPicked
                        ? "border-emerald-500/30 bg-emerald-500/20"
                        : "border-[var(--border-subtle)] bg-[var(--bg-tertiary)]"
                    )}
                  >
                    <span
                      className={cn(
                        "inline-block h-3.5 w-3.5 rounded-full bg-white shadow transition-transform",
                        isPicked ? "translate-x-4" : "translate-x-0.5"
                      )}
                    />
                  </button>
                  <span
                    className={cn(
                      "truncate font-mono",
                      isPicked ? "text-[var(--text-primary)]" : "text-[var(--text-tertiary)]"
                    )}
                  >
                    {e.id}
                  </span>
                  <span
                    className={cn(
                      "truncate",
                      isPicked ? "text-[var(--text-secondary)]" : "text-[var(--text-tertiary)]"
                    )}
                  >
                    {e.label}
                  </span>
                  {type === "discord" ? (
                    <MentionPicker
                      value={mentions[e.id] ?? ""}
                      onChange={(v) =>
                        setMentions((cur) => ({ ...cur, [e.id]: v }))
                      }
                      defaultMention={defaultMentionFor(e.id)}
                      disabled={!isPicked}
                    />
                  ) : (
                    <span />
                  )}
                </div>
              );
            })}
            <p className="font-mono text-[10px] text-[var(--text-tertiary)]">
              {events.length === 0
                ? "No events selected — channel will receive nothing."
                : `${events.length} event${events.length === 1 ? "" : "s"} selected.`}
            </p>
          </div>
        </Field>
        <Field label="enabled">
          <button
            type="button"
            onClick={() => setEnabled((v) => !v)}
            className={cn(
              "inline-flex h-6 w-10 items-center rounded-full border transition-colors",
              enabled
                ? "border-emerald-500/30 bg-emerald-500/20"
                : "border-[var(--border-subtle)] bg-[var(--bg-tertiary)]"
            )}
          >
            <span
              className={cn(
                "inline-block h-4 w-4 rounded-full bg-white shadow transition-transform",
                enabled ? "translate-x-5" : "translate-x-1"
              )}
            />
          </button>
        </Field>
      </div>
      <footer className="mt-4 flex items-center justify-end gap-2">
        <Button variant="ghost" size="sm" onClick={onClose} disabled={save.isPending}>
          Cancel
        </Button>
        <Button
          size="sm"
          onClick={() => save.mutate()}
          disabled={save.isPending || !name || !url}
        >
          {save.isPending ? "Saving…" : isNew ? "Create" : "Save"}
        </Button>
      </footer>
    </div>
  );
}

function Field({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="grid grid-cols-[140px_1fr] items-start gap-3">
      <div>
        <div className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
          {label}
        </div>
        {hint && <div className="mt-0.5 text-[10px] text-[var(--text-tertiary)]/70">{hint}</div>}
      </div>
      <div>{children}</div>
    </div>
  );
}

// MentionPicker is the per-event Discord mention selector. Five
// options: server default (which is @here for outage events,
// nothing otherwise), explicit none, @here, @everyone, custom role.
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
  // "role:<id>", or any other custom string. We map to a select on
  // the common cases + an inline input when "role:" is picked.
  const [showRole, setShowRole] = useState(value.startsWith("role:"));
  const roleID = value.startsWith("role:") ? value.slice("role:".length) : "";

  const options: { v: string; label: string }[] = [
    { v: "", label: defaultMention ? `default (${defaultMention})` : "default (none)" },
    { v: "none", label: "none" },
    { v: "@here", label: "@here" },
    { v: "@everyone", label: "@everyone" },
    { v: "role:", label: "role…" },
  ];
  return (
    <div className={cn("flex items-center gap-1", disabled && "pointer-events-none opacity-40")}>
      <select
        value={showRole ? "role:" : value}
        onChange={(e) => {
          const v = e.target.value;
          if (v === "role:") {
            setShowRole(true);
            onChange("role:" + roleID);
          } else {
            setShowRole(false);
            onChange(v);
          }
        }}
        disabled={disabled}
        className="h-6 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-1 font-mono text-[10px] text-[var(--text-primary)]"
      >
        {options.map((o) => (
          <option key={o.v} value={o.v}>
            {o.label}
          </option>
        ))}
      </select>
      {showRole && (
        <input
          value={roleID}
          onChange={(e) => onChange("role:" + e.target.value)}
          placeholder="role ID"
          disabled={disabled}
          className="h-6 w-28 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-1.5 font-mono text-[10px]"
          spellCheck={false}
        />
      )}
    </div>
  );
}

// defaultMentionFor mirrors the server-side notify.mentionFor logic
// so the UI's "default (...)" label tells the truth. Outage events
// (anything error-severity in the server's view) default to @here.
function defaultMentionFor(eventID: string): string {
  switch (eventID) {
    case "build.failed":
    case "pod.crashed":
    case "alert.fired":
    case "backup.failed":
      return "@here";
    default:
      return "";
  }
}
