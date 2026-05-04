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
  type AlertRule,
  type CreateAlertBody,
} from "@/features/alerts";
import { toast } from "sonner";
import { relativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";

// /settings/alerts — manage alert rules. Engine evaluates them on a
// 1-min ticker server-side and fires through the existing notify
// dispatcher (Discord/webhook/Slack). UI surfaces the rule list +
// add/delete/enable-disable + a tiny lastFired timestamp so the
// user can see "this rule has been firing".
export default function AlertsPage() {
  const qc = useQueryClient();
  const list = useQuery({ queryKey: ["alerts"], queryFn: listAlerts });
  const [adding, setAdding] = useState<"log" | "node" | null>(null);

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
          <div className="flex items-center gap-2">
            <Button size="sm" variant="outline" onClick={() => setAdding("log")}>
              <Plus className="h-3.5 w-3.5" /> Log match
            </Button>
            <Button size="sm" variant="outline" onClick={() => setAdding("node")}>
              <Plus className="h-3.5 w-3.5" /> Node pressure
            </Button>
          </div>
        </CardHeader>
        <CardContent>
          {list.isPending ? (
            <Skeleton className="h-24 w-full" />
          ) : list.isError ? (
            <p className="font-mono text-[11px] text-red-400">
              Failed to load: {list.error instanceof Error ? list.error.message : "unknown"}
            </p>
          ) : (list.data ?? []).length === 0 ? (
            <p className="rounded-md border border-dashed border-[var(--border-subtle)] px-3 py-6 text-center text-[12px] text-[var(--text-tertiary)]">
              No rules yet. Click <span className="font-mono">+ Log match</span> or{" "}
              <span className="font-mono">+ Node pressure</span> to add one.
            </p>
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
          kind={adding}
          onClose={() => setAdding(null)}
          onCreated={() => {
            setAdding(null);
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
  const detail = (() => {
    if (rule.kind === "log_match") {
      return `${rule.query ?? ""} ≥ ${rule.thresholdInt ?? 1} in ${formatSec(rule.windowSeconds)}`;
    }
    return `≥ ${rule.thresholdFloat ?? 80}% (window ${formatSec(rule.windowSeconds)})`;
  })();
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
            </span>
          )}
        </div>
        <p className="truncate font-mono text-[10px] text-[var(--text-tertiary)]">{detail}</p>
        {rule.lastFiredAt && (
          <p className="font-mono text-[10px] text-amber-400">
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
        <div className="inline-flex items-center gap-1 rounded border border-red-500/30 bg-red-500/5 px-1.5 py-0.5">
          <Button size="sm" variant="ghost" onClick={onDelete} className="h-5 px-1 text-[10px] text-red-400">
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
          className="rounded p-1 text-[var(--text-tertiary)] hover:bg-red-500/10 hover:text-red-400"
        >
          <Trash2 className="h-3.5 w-3.5" />
        </button>
      )}
    </li>
  );
}

function AddRuleDialog({
  kind,
  onClose,
  onCreated,
}: {
  kind: "log" | "node";
  onClose: () => void;
  onCreated: () => void;
}) {
  const [name, setName] = useState("");
  const [project, setProject] = useState("");
  const [service, setService] = useState("");
  const [query, setQuery] = useState("");
  const [thresholdInt, setThresholdInt] = useState("1");
  const [thresholdPct, setThresholdPct] = useState("80");
  const [nodeKind, setNodeKind] = useState<"node_cpu" | "node_mem" | "node_disk">("node_cpu");
  const [window, setWindow] = useState("5m");
  const [severity, setSeverity] = useState<"info" | "warn" | "error">("warn");
  const [throttle, setThrottle] = useState("10m");

  const create = useMutation({
    mutationFn: () => {
      const body: CreateAlertBody = {
        name: name.trim(),
        kind: kind === "log" ? "log_match" : nodeKind,
        windowSeconds: parseDur(window),
        severity,
        throttleSeconds: parseDur(throttle),
      };
      if (kind === "log") {
        body.project = project.trim() || undefined;
        body.service = service.trim() || undefined;
        body.query = query.trim();
        body.thresholdInt = parseInt(thresholdInt, 10);
      } else {
        body.thresholdFloat = parseFloat(thresholdPct);
      }
      return createAlert(body);
    },
    onSuccess: () => {
      toast.success(`Alert ${name} created`);
      onCreated();
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Create failed"),
  });

  const submitDisabled =
    !name.trim() ||
    create.isPending ||
    (kind === "log" && !query.trim());

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
            <h2 className="font-mono text-sm font-medium">
              New {kind === "log" ? "log-match" : "node-pressure"} alert
            </h2>
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
          <Field label="Name">
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={kind === "log" ? "OOMKilled" : "CPU > 90%"}
              className="h-8 text-[13px]"
            />
          </Field>

          {kind === "log" ? (
            <>
              <Field label="FTS5 query">
                <Input
                  value={query}
                  onChange={(e) => setQuery(e.target.value)}
                  placeholder='OOMKilled    OR    "fatal error"'
                  className="h-8 font-mono text-[12px]"
                />
              </Field>
              <div className="grid grid-cols-2 gap-3">
                <Field label="Project (optional)">
                  <Input
                    value={project}
                    onChange={(e) => setProject(e.target.value)}
                    placeholder="myproj"
                    className="h-8 font-mono text-[12px]"
                  />
                </Field>
                <Field label="Service (optional)">
                  <Input
                    value={service}
                    onChange={(e) => setService(e.target.value)}
                    placeholder="api"
                    className="h-8 font-mono text-[12px]"
                  />
                </Field>
              </div>
              <Field label="Threshold (matches)">
                <Input
                  type="number"
                  value={thresholdInt}
                  onChange={(e) => setThresholdInt(e.target.value)}
                  className="h-8 font-mono text-[12px]"
                />
              </Field>
            </>
          ) : (
            <>
              <Field label="Resource">
                <select
                  value={nodeKind}
                  onChange={(e) => setNodeKind(e.target.value as typeof nodeKind)}
                  className="h-8 w-full rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 font-mono text-[12px]"
                >
                  <option value="node_cpu">CPU</option>
                  <option value="node_mem">Memory</option>
                  <option value="node_disk">Disk</option>
                </select>
              </Field>
              <Field label="Threshold (%)">
                <Input
                  type="number"
                  value={thresholdPct}
                  onChange={(e) => setThresholdPct(e.target.value)}
                  className="h-8 font-mono text-[12px]"
                />
              </Field>
            </>
          )}

          <div className="grid grid-cols-3 gap-3">
            <Field label="Window">
              <Input
                value={window}
                onChange={(e) => setWindow(e.target.value)}
                placeholder="5m"
                className="h-8 font-mono text-[12px]"
              />
            </Field>
            <Field label="Throttle">
              <Input
                value={throttle}
                onChange={(e) => setThrottle(e.target.value)}
                placeholder="10m"
                className="h-8 font-mono text-[12px]"
              />
            </Field>
            <Field label="Severity">
              <select
                value={severity}
                onChange={(e) => setSeverity(e.target.value as typeof severity)}
                className="h-8 w-full rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 font-mono text-[12px]"
              >
                <option value="info">info</option>
                <option value="warn">warn</option>
                <option value="error">error</option>
              </select>
            </Field>
          </div>
        </div>
        <footer className="flex items-center justify-end gap-2 border-t border-[var(--border-subtle)] px-4 py-3">
          <Button size="sm" variant="ghost" onClick={onClose} disabled={create.isPending}>
            Cancel
          </Button>
          <Button size="sm" disabled={submitDisabled} onClick={() => create.mutate()}>
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

// parseDur: "5m" / "30s" / "300" → seconds. Invalid → 0 (server applies default).
function parseDur(s: string): number {
  const m = s.match(/^(\d+)\s*([smhd]?)$/);
  if (!m) return 0;
  const n = parseInt(m[1], 10);
  switch (m[2]) {
    case "":
    case "s":
      return n;
    case "m":
      return n * 60;
    case "h":
      return n * 3600;
    case "d":
      return n * 86_400;
  }
  return 0;
}

function formatSec(n: number): string {
  if (n >= 86_400) return `${Math.round(n / 86_400)}d`;
  if (n >= 3600) return `${Math.round(n / 3600)}h`;
  if (n >= 60) return `${Math.round(n / 60)}m`;
  return `${n}s`;
}
