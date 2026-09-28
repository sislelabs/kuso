"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Bell, Plus, Trash2, X } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import {
  listAlerts,
  createAlert,
  deleteAlert,
  enableAlert,
  disableAlert,
  ALERT_KINDS,
  buildCreateBody,
  describeRule,
  emptyRuleForm,
  type AlertKind,
  type AlertRule,
  type RuleFormState,
} from "@/features/alerts";
import { toast } from "sonner";
import { relativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import { EmptyState } from "@/components/shared/EmptyState";

// /settings/alerts — manage alert rules. Engine evaluates them on a
// 1-min ticker server-side and fires through the existing notify
// dispatcher (Discord/webhook/Slack). UI surfaces the rule list +
// add/delete/enable-disable + a tiny lastFired timestamp so the
// user can see "this rule has been firing".
export default function AlertsPage() {
  const qc = useQueryClient();
  const list = useQuery({ queryKey: ["alerts"], queryFn: listAlerts });
  const [adding, setAdding] = useState(false);

  const del = useMutation({
    mutationFn: (id: string) => deleteAlert(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["alerts"] }),
    onError: (e) => toast.error(e instanceof Error ? e.message : "Delete failed"),
  });
  const toggle = useMutation({
    mutationFn: ({ id, on }: { id: string; on: boolean }) =>
      on ? enableAlert(id) : disableAlert(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["alerts"] }),
    onError: (e) => toast.error(e instanceof Error ? e.message : "Toggle failed"),
  });

  return (
    <div className="mx-auto max-w-3xl space-y-6 p-6 lg:p-8">
      <header className="flex items-start justify-between gap-4">
        <div>
          <h1 className="font-heading text-2xl font-semibold tracking-tight">Alert rules</h1>
          <p className="mt-1 text-sm text-[var(--text-secondary)]">
            Evaluated every minute. Fires through your configured notification channels
            (Discord, webhook, Slack — set up in <span className="font-mono">/settings/notifications</span>).
          </p>
        </div>
        <Bell className="h-6 w-6 shrink-0 text-[var(--text-tertiary)]" />
      </header>

      {/* Rule list */}
      <Card>
        <CardHeader className="flex-row items-center justify-between">
          <CardTitle>Rules</CardTitle>
          <Button size="sm" variant="outline" onClick={() => setAdding(true)}>
            <Plus className="h-3.5 w-3.5" /> New rule
          </Button>
        </CardHeader>
        <CardContent>
          {list.isPending ? (
            <Skeleton className="h-24 w-full" />
          ) : list.isError ? (
            <p className="font-mono text-[11px] text-[var(--error)]">
              Failed to load: {list.error instanceof Error ? list.error.message : "unknown"}
            </p>
          ) : (list.data ?? []).length === 0 ? (
            <EmptyState
              icon={<Bell className="h-5 w-5" />}
              title="No rules yet"
              description="Click + New rule to watch 5xx rates, latency, certificates, DNS, logs or nodes."
              className="px-3 py-6"
            />
          ) : (
            <ul className="divide-y divide-[var(--border-subtle)]">
              {(list.data ?? []).map((r) => (
                <RuleRow
                  key={r.id}
                  rule={r}
                  onDelete={() => del.mutate(r.id)}
                  onToggle={(on) => toggle.mutate({ id: r.id, on })}
                />
              ))}
            </ul>
          )}
        </CardContent>
      </Card>

      {adding && (
        <AddRuleDialog
          onClose={() => setAdding(false)}
          onCreated={() => {
            setAdding(false);
            qc.invalidateQueries({ queryKey: ["alerts"] });
          }}
        />
      )}
    </div>
  );
}

function RuleRow({
  rule,
  onDelete,
  onToggle,
}: {
  rule: AlertRule;
  onDelete: () => void;
  onToggle: (on: boolean) => void;
}) {
  const [confirming, setConfirming] = useState(false);
  const detail = describeRule(rule);
  return (
    <li className="flex items-center gap-3 px-1 py-2">
      <span
        aria-hidden
        className={cn(
          "mt-0.5 inline-block h-1.5 w-1.5 shrink-0 rounded-full",
          rule.severity === "error" ? "bg-red-400" : rule.severity === "warn" ? "bg-amber-400" : "bg-emerald-400"
        )}
      />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <p className="truncate text-sm font-medium">{rule.name}</p>
          <span className="rounded bg-[var(--bg-tertiary)] px-1.5 py-0.5 font-mono text-[10px] text-[var(--text-tertiary)]">
            {rule.kind}
          </span>
          {rule.project && (
            <span className="font-mono text-[10px] text-[var(--text-tertiary)]">
              {rule.project}
              {rule.service && `/${rule.service}`}
              {rule.env && ` → ${rule.env}`}
            </span>
          )}
        </div>
        <p className="truncate font-mono text-[10px] text-[var(--text-tertiary)]">{detail}</p>
        {rule.firingSince ? (
          <p className="truncate font-mono text-[10px] text-[var(--error)]">
            firing since {relativeTime(rule.firingSince)}
            {rule.firingTargets && rule.firingTargets.length > 0 && ` · ${rule.firingTargets.join(", ")}`}
          </p>
        ) : rule.lastFiredAt && (
          <p className="font-mono text-[10px] text-[var(--warning)]">
            last fired {relativeTime(rule.lastFiredAt)}
          </p>
        )}
      </div>
      <label className="flex cursor-pointer items-center gap-1 font-mono text-[10px] text-[var(--text-tertiary)]">
        <input
          type="checkbox"
          checked={rule.enabled}
          onChange={(e) => onToggle(e.target.checked)}
          className="accent-[var(--accent)]"
        />
        enabled
      </label>
      {confirming ? (
        <div className="inline-flex items-center gap-1 rounded border border-[var(--error)]/30 bg-[var(--error-subtle)] px-1.5 py-0.5">
          <Button size="sm" variant="ghost" onClick={onDelete} className="h-5 px-1 text-[10px] text-[var(--error)]">
            yes
          </Button>
          <Button
            size="sm"
            variant="ghost"
            onClick={() => setConfirming(false)}
            className="h-5 px-1 text-[10px]"
          >
            no
          </Button>
        </div>
      ) : (
        <button
          type="button"
          onClick={() => setConfirming(true)}
          className="rounded p-1 text-[var(--text-tertiary)] hover:bg-[var(--error-subtle)] hover:text-[var(--error)]"
        >
          <Trash2 className="h-3.5 w-3.5" />
        </button>
      )}
    </li>
  );
}

const KIND_GROUPS = ["Service", "Edge", "Logs", "Nodes"] as const;

function AddRuleDialog({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const [form, setForm] = useState<RuleFormState>(() => emptyRuleForm("http_5xx_rate"));
  const meta = ALERT_KINDS[form.kind];
  const set = <K extends keyof RuleFormState>(k: K, v: RuleFormState[K]) => setForm((f) => ({ ...f, [k]: v }));
  const built = buildCreateBody(form);

  const create = useMutation({
    mutationFn: () => {
      if (!built.ok) throw new Error(built.error);
      return createAlert(built.body);
    },
    onSuccess: () => {
      toast.success(`Alert ${form.name.trim()} created`);
      onCreated();
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Create failed"),
  });

  const changeKind = (kind: AlertKind) =>
    setForm((f) => ({ ...emptyRuleForm(kind), name: f.name, project: f.project, service: f.service, severity: f.severity }));

  return (
    <div
      role="dialog"
      aria-modal="true"
      onClick={onClose}
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-6"
    >
      <div
        onClick={(e) => e.stopPropagation()}
        className="w-full max-w-lg rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] shadow-[var(--shadow-lg)]"
      >
        <header className="flex items-center justify-between border-b border-[var(--border-subtle)] px-4 py-3">
          <div>
            <h2 className="font-mono text-sm font-medium">New alert rule</h2>
            <p className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
              evaluated every 1 min · fires through configured channels
            </p>
          </div>
          <button
            type="button"
            onClick={onClose}
            className="rounded p-1 text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
          >
            <X className="h-4 w-4" />
          </button>
        </header>
        <div className="space-y-3 p-4">
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <Field label="Kind">
              <select
                value={form.kind}
                onChange={(e) => changeKind(e.target.value as AlertKind)}
                className="h-8 w-full rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 font-mono text-[12px]"
              >
                {KIND_GROUPS.map((g) => (
                  <optgroup key={g} label={g}>
                    {(Object.keys(ALERT_KINDS) as AlertKind[])
                      .filter((k) => ALERT_KINDS[k].group === g)
                      .map((k) => (
                        <option key={k} value={k}>
                          {ALERT_KINDS[k].label}
                        </option>
                      ))}
                  </optgroup>
                ))}
              </select>
            </Field>
            <Field label="Name">
              <Input
                value={form.name}
                onChange={(e) => set("name", e.target.value)}
                placeholder={meta.label}
                className="h-8 text-[13px]"
              />
            </Field>
          </div>
          <p className="text-[12px] text-[var(--text-secondary)]">{meta.help}</p>

          {form.kind === "log_match" && (
            <Field label="Query (substring)">
              <Input
                value={form.query}
                onChange={(e) => set("query", e.target.value)}
                placeholder="OOMKilled"
                className="h-8 font-mono text-[12px]"
              />
            </Field>
          )}

          {meta.scope !== "none" && (
            <div className={cn("grid grid-cols-1 gap-3", meta.scope === "env" ? "sm:grid-cols-3" : "sm:grid-cols-2")}>
              <Field label="Project (optional)">
                <Input
                  value={form.project}
                  onChange={(e) => set("project", e.target.value)}
                  placeholder="all projects"
                  className="h-8 font-mono text-[12px]"
                />
              </Field>
              <Field label="Service (optional)">
                <Input
                  value={form.service}
                  onChange={(e) => set("service", e.target.value)}
                  placeholder="all services"
                  className="h-8 font-mono text-[12px]"
                />
              </Field>
              {meta.scope === "env" && (
                <Field label="Env (optional)">
                  <Input
                    value={form.env}
                    onChange={(e) => set("env", e.target.value)}
                    placeholder="all envs"
                    className="h-8 font-mono text-[12px]"
                  />
                </Field>
              )}
            </div>
          )}

          {(meta.threshold || meta.minRequests) && (
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
              {meta.threshold && (
                <Field label={meta.threshold.label}>
                  <Input
                    type="number"
                    value={form.threshold}
                    onChange={(e) => set("threshold", e.target.value)}
                    placeholder={meta.threshold.placeholder}
                    className="h-8 font-mono text-[12px]"
                  />
                </Field>
              )}
              {meta.minRequests && (
                <Field label="Min requests in window">
                  <Input
                    type="number"
                    value={form.minRequests}
                    onChange={(e) => set("minRequests", e.target.value)}
                    placeholder="20"
                    className="h-8 font-mono text-[12px]"
                  />
                </Field>
              )}
            </div>
          )}

          <div className={cn("grid grid-cols-1 gap-3", meta.window ? "sm:grid-cols-3" : "sm:grid-cols-2")}>
            {meta.window && (
              <Field label="Window">
                <Input
                  value={form.window}
                  onChange={(e) => set("window", e.target.value)}
                  placeholder="5m"
                  className="h-8 font-mono text-[12px]"
                />
              </Field>
            )}
            <Field label="Throttle">
              <Input
                value={form.throttle}
                onChange={(e) => set("throttle", e.target.value)}
                placeholder="10m"
                className="h-8 font-mono text-[12px]"
              />
            </Field>
            <Field label="Severity">
              <select
                value={form.severity}
                onChange={(e) => set("severity", e.target.value as RuleFormState["severity"])}
                className="h-8 w-full rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 font-mono text-[12px]"
              >
                <option value="info">info</option>
                <option value="warn">warn</option>
                <option value="error">error</option>
              </select>
            </Field>
          </div>
          {!built.ok && form.name.trim() !== "" && (
            <p className="font-mono text-[11px] text-[var(--error)]">{built.error}</p>
          )}
        </div>
        <footer className="flex items-center justify-end gap-2 border-t border-[var(--border-subtle)] px-4 py-3">
          <Button size="sm" variant="ghost" onClick={onClose} disabled={create.isPending}>
            Cancel
          </Button>
          <Button size="sm" disabled={!built.ok || create.isPending} onClick={() => create.mutate()}>
            {create.isPending ? "Creating…" : "Create rule"}
          </Button>
        </footer>
      </div>
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="space-y-1 block">
      <span className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
        {label}
      </span>
      {children}
    </label>
  );
}
