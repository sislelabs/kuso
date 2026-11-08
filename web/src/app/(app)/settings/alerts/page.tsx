"use client";

import { Suspense, useEffect, useState } from "react";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Bell, Plus, Trash2, X } from "lucide-react";
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
  prefillRuleForm,
  type AlertKind,
  type AlertRule,
  type RuleFormState,
} from "@/features/alerts";
import { toast } from "sonner";
import { toastError } from "@/lib/errors";
import { Switch } from "@/components/ui/switch";
import { relativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import { EmptyState } from "@/components/shared/EmptyState";
import { Dialog, DialogContent, DialogTitle } from "@/components/ui/dialog";
import Link from "next/link";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { QueryErrorState } from "@/components/shared/QueryErrorState";
import { SettingsPage } from "@/components/settings/SettingsPage";
import { SettingsSection, SettingsSelect } from "@/components/settings/SettingsSection";

// /settings/alerts — manage alert rules. Engine evaluates them on a
// 1-min ticker server-side and fires through the existing notify
// dispatcher (Discord/webhook/Slack). UI surfaces the rule list +
// add/delete/enable-disable + a tiny lastFired timestamp so the
// user can see "this rule has been firing".
export default function AlertsPage() {
  return (
    <Suspense fallback={null}>
      <AlertsPageInner />
    </Suspense>
  );
}

function AlertsPageInner() {
  const qc = useQueryClient();
  const list = useQuery({ queryKey: ["alerts"], queryFn: listAlerts });
  // null = closed; a form = open with that starting state.
  const [adding, setAdding] = useState<RuleFormState | null>(null);
  const search = useSearchParams();
  const router = useRouter();
  const pathname = usePathname();

  // Deep link: ?new=1&project=&service=&kind=&env= opens a prefilled
  // create form. `new` is stripped so a reload doesn't reopen it.
  useEffect(() => {
    if (!search) return;
    const prefill = prefillRuleForm(new URLSearchParams(search.toString()));
    if (!prefill) return;
    setAdding(prefill);
    const next = new URLSearchParams(search.toString());
    for (const k of ["new", "kind", "project", "service", "env"]) next.delete(k);
    const qs = next.toString();
    router.replace(qs ? `${pathname}?${qs}` : (pathname ?? "/settings/alerts"), { scroll: false });
  }, [search, router, pathname]);

  const del = useMutation({
    mutationFn: (id: string) => deleteAlert(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["alerts"] }),
    onError: (e, id) => toastError(e, "Couldn't delete the alert rule", { retry: () => del.mutate(id) }),
  });
  const toggle = useMutation({
    mutationFn: ({ id, on }: { id: string; on: boolean }) =>
      on ? enableAlert(id) : disableAlert(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["alerts"] }),
    onError: (e, vars) =>
      toastError(e, vars.on ? "Couldn't enable the alert rule" : "Couldn't disable the alert rule", {
        retry: () => toggle.mutate(vars),
      }),
  });

  return (
    <SettingsPage
      title="Alert rules"
      description={
        <>
          Evaluated every minute. Fires through your{" "}
          <Link href="/settings/notifications" className="text-[var(--text-primary)] underline underline-offset-2">
            notification channels
          </Link>
          .
        </>
      }
      actions={
        <Button size="sm" onClick={() => setAdding(emptyRuleForm("http_5xx_rate"))}>
          <Plus className="h-3.5 w-3.5" /> New rule
        </Button>
      }
    >
      {list.isPending ? (
        <Skeleton className="h-32 w-full rounded-md" />
      ) : list.isError ? (
        <QueryErrorState what="alert rules" error={list.error} onRetry={() => void list.refetch()} />
      ) : (list.data ?? []).length === 0 ? (
        <EmptyState
          icon={<Bell className="h-5 w-5" />}
          title="No rules yet"
          description="Click New rule to watch 5xx rates, latency, certificates, DNS, logs or nodes."
          className="p-8"
        />
      ) : (
        <SettingsSection
          title="Rules"
          description={`${(list.data ?? []).length} ${(list.data ?? []).length === 1 ? "rule" : "rules"}. Disabled rules aren't evaluated.`}
          flush
        >
          <ul className="divide-y divide-[var(--border-subtle)]">
            {(list.data ?? []).map((r) => (
              <RuleRow
                key={r.id}
                rule={r}
                deleting={del.isPending && del.variables === r.id}
                onDelete={() => del.mutate(r.id)}
                onToggle={(on) => toggle.mutate({ id: r.id, on })}
              />
            ))}
          </ul>
        </SettingsSection>
      )}

      {adding && (
        <AddRuleDialog
          initial={adding}
          onClose={() => setAdding(null)}
          onCreated={() => {
            setAdding(null);
            qc.invalidateQueries({ queryKey: ["alerts"] });
          }}
        />
      )}
    </SettingsPage>
  );
}

function RuleRow({
  rule,
  deleting,
  onDelete,
  onToggle,
}: {
  rule: AlertRule;
  deleting: boolean;
  onDelete: () => void;
  onToggle: (on: boolean) => void;
}) {
  const [confirming, setConfirming] = useState(false);
  const detail = describeRule(rule);
  return (
    <li className="flex flex-wrap items-start gap-x-3 gap-y-2 px-4 py-3 sm:flex-nowrap sm:px-5">
      <span
        aria-hidden
        className={cn(
          "mt-1.5 inline-block h-2 w-2 shrink-0 rounded-full",
          rule.severity === "error"
            ? "bg-[var(--error)]"
            : rule.severity === "warn"
              ? "bg-[var(--warning)]"
              : "bg-[var(--success)]"
        )}
        title={`Severity: ${rule.severity}`}
      />
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
          <p className="truncate text-sm font-medium text-[var(--text-primary)]">{rule.name}</p>
          <span className="rounded-sm bg-[var(--bg-tertiary)] px-1.5 py-0.5 font-mono text-xs text-[var(--text-secondary)]">
            {rule.kind}
          </span>
          {rule.project && (
            <span className="font-mono text-xs text-[var(--text-secondary)]">
              {rule.project}
              {rule.service && `/${rule.service}`}
              {rule.env && ` → ${rule.env}`}
            </span>
          )}
        </div>
        <p className="mt-0.5 text-xs text-[var(--text-secondary)]">{detail}</p>
        {rule.firingSince ? (
          <p className="mt-0.5 text-xs text-[var(--error)]">
            Firing since {relativeTime(rule.firingSince)}
            {rule.firingTargets && rule.firingTargets.length > 0 && (
              <span className="font-mono"> · {rule.firingTargets.join(", ")}</span>
            )}
          </p>
        ) : rule.lastFiredAt && (
          <p className="mt-0.5 text-xs text-[var(--warning)]">
            Last fired {relativeTime(rule.lastFiredAt)}
          </p>
        )}
      </div>
      <div className="flex w-full items-center justify-end gap-3 sm:w-auto">
        <span className="flex items-center gap-2 text-xs text-[var(--text-secondary)]">
          <span aria-hidden>{rule.enabled ? "Enabled" : "Disabled"}</span>
          <Switch on={rule.enabled} onChange={onToggle} size="sm" label={`Evaluate ${rule.name}`} />
        </span>
        <Button
          variant="ghost"
          size="icon-sm"
          onClick={() => setConfirming(true)}
          aria-label={`Delete ${rule.name}`}
          className="text-[var(--text-tertiary)] hover:text-[var(--error)]"
        >
          <Trash2 className="h-3.5 w-3.5" />
        </Button>
      </div>
      <ConfirmDialog
        open={confirming}
        title={`Delete alert rule ${rule.name}?`}
        body={<p>The rule stops being evaluated and its firing state is discarded.</p>}
        confirmLabel="Delete rule"
        typeToConfirm={rule.name}
        destructive
        pending={deleting}
        onConfirm={() => {
          onDelete();
          setConfirming(false);
        }}
        onCancel={() => setConfirming(false)}
      />
    </li>
  );
}

const KIND_GROUPS = ["Service", "Edge", "Logs", "Nodes"] as const;

function AddRuleDialog({
  initial,
  onClose,
  onCreated,
}: {
  initial: RuleFormState;
  onClose: () => void;
  onCreated: () => void;
}) {
  const [initialForm] = useState<RuleFormState>(initial);
  const [form, setForm] = useState<RuleFormState>(initialForm);
  const dirty = JSON.stringify(form) !== JSON.stringify(initialForm);
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
    onError: (e) => toastError(e, "Couldn't create the alert rule"),
  });

  const changeKind = (kind: AlertKind) =>
    setForm((f) => ({ ...emptyRuleForm(kind), name: f.name, project: f.project, service: f.service, severity: f.severity }));

  return (
    <Dialog
      open
      onOpenChange={(next, details) => {
        if (next || create.isPending) return;
        // A stray backdrop click shouldn't throw away a half-built rule.
        if (dirty && details.reason === "outside-press") return;
        onClose();
      }}
    >
      <DialogContent
        showCloseButton={false}
        className="max-h-[calc(100dvh-2rem)] max-w-lg gap-0 overflow-y-auto rounded-md bg-[var(--bg-secondary)] p-0 sm:max-w-lg"
      >
        <header className="flex items-center justify-between border-b border-[var(--border-subtle)] px-4 py-3">
          <div>
            <DialogTitle className="text-base font-semibold">New alert rule</DialogTitle>
            <p className="mt-0.5 text-xs text-[var(--text-secondary)]">
              Evaluated every minute. Fires through your notification channels.
            </p>
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label="Close"
            className="rounded p-1 text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
          >
            <X className="h-4 w-4" />
          </button>
        </header>
        <div className="space-y-4 p-4">
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <Field label="Kind">
              <SettingsSelect
                value={form.kind}
                onChange={(e) => changeKind(e.target.value as AlertKind)}
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
              </SettingsSelect>
            </Field>
            <Field label="Name">
              <Input
                value={form.name}
                onChange={(e) => set("name", e.target.value)}
                placeholder={meta.label}
              />
            </Field>
          </div>
          <p className="text-xs leading-relaxed text-[var(--text-secondary)]">{meta.help}</p>

          {form.kind === "log_match" && (
            <Field label="Query (substring)">
              <Input
                value={form.query}
                onChange={(e) => set("query", e.target.value)}
                placeholder="OOMKilled"
                className="font-mono"
              />
            </Field>
          )}

          {meta.scope !== "none" && (
            <div className={cn("grid grid-cols-1 gap-3", meta.scope === "env" ? "sm:grid-cols-3" : "sm:grid-cols-2")}>
              <Field label="Project (optional)">
                <Input
                  value={form.project}
                  onChange={(e) => set("project", e.target.value)}
                  placeholder="All projects"
                  className="font-mono"
                />
              </Field>
              <Field label="Service (optional)">
                <Input
                  value={form.service}
                  onChange={(e) => set("service", e.target.value)}
                  placeholder="All services"
                  className="font-mono"
                />
              </Field>
              {meta.scope === "env" && (
                <Field label="Env (optional)">
                  <Input
                    value={form.env}
                    onChange={(e) => set("env", e.target.value)}
                    placeholder="All envs"
                    className="font-mono"
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
                    className="font-mono"
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
                    className="font-mono"
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
                  className="font-mono"
                />
              </Field>
            )}
            <Field label="Throttle">
              <Input
                value={form.throttle}
                onChange={(e) => set("throttle", e.target.value)}
                placeholder="10m"
                className="font-mono"
              />
            </Field>
            <Field label="Severity">
              <SettingsSelect
                value={form.severity}
                onChange={(e) => set("severity", e.target.value as RuleFormState["severity"])}
              >
                <option value="info">Info</option>
                <option value="warn">Warning</option>
                <option value="error">Error</option>
              </SettingsSelect>
            </Field>
          </div>
          {!built.ok && form.name.trim() !== "" && (
            <p className="text-xs text-[var(--error)]">{built.error}</p>
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
      </DialogContent>
    </Dialog>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="block space-y-1.5">
      <span className="block text-sm font-medium text-[var(--text-primary)]">{label}</span>
      {children}
    </label>
  );
}
