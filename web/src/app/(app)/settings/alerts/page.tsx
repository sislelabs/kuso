"use client";

import { Suspense, useEffect, useMemo, useState } from "react";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Bell, Lock, Plus, Trash2, X } from "lucide-react";
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
  alertKindLabel,
  buildCreateBody,
  describeRule,
  emptyRuleForm,
  prefillRuleForm,
  alertKindsFor,
  alertRequestText,
  canManageAlertRule,
  editableAlertProjects,
  type AlertAccessSession,
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
import { Callout } from "@/components/ui/callout";
import { CopyButton } from "@/components/ui/copy-button";
import { useSession } from "@/features/auth";
import { useEnvironments, useProjects, useServices } from "@/features/projects";
import { envGroupName } from "@/lib/env-group";
import { api, ApiError } from "@/lib/api-client";
import { serviceShortName } from "@/lib/utils";

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
  const session = useSession();
  const access: AlertAccessSession = useMemo(
    () => ({
      adminAll: session.data?.session.adminAll ?? false,
      projectRoles: session.data?.session.projectRoles ?? {},
    }),
    [session.data],
  );
  const projects = useProjects();
  const editable = useMemo(
    () => editableAlertProjects(access, (projects.data ?? []).map((p) => p.metadata.name)),
    [access, projects.data],
  );
  const isAdmin = access.adminAll;
  // Servers before project-scoped alert access answer 403 to anyone
  // but an instance admin.
  const adminOnlyServer = !isAdmin && list.error instanceof ApiError && list.error.status === 403;
  const canAdd = isAdmin || editable.length > 0;
  const addBlockedReason = session.isPending
    ? undefined
    : adminOnlyServer
      ? "Alert rules are admin-only on this server"
      : !canAdd
        ? "You need editor access on a project to add its alert rules"
        : undefined;
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

  // Delivery backlog for the channels alerts fire through. The server
  // counts it instance-wide (not per rule) and only for admins, so a
  // failure here just hides the hint.
  const outbox = useQuery({
    queryKey: ["admin", "notifications", "outbox-stats"],
    queryFn: () => api<{ pending: number; dead: number }>("/api/notifications/outbox-stats"),
    enabled: isAdmin,
    retry: false,
    refetchInterval: 30_000,
  });
  const deliveryFailing = (outbox.data?.pending ?? 0) + (outbox.data?.dead ?? 0) > 0;

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
        <Button
          size="sm"
          disabled={!!addBlockedReason}
          title={addBlockedReason}
          onClick={() => setAdding(emptyRuleForm("http_5xx_rate"))}
        >
          <Plus className="h-3.5 w-3.5" /> New rule
        </Button>
      }
    >
      {!isAdmin && session.data && !adminOnlyServer && (
        <Callout tone="neutral" className="mb-4">
          {editable.length > 0 ? (
            <>
              You can add and change rules for projects you can edit ({editable.join(", ")}). Cluster-wide and node
              rules are managed by an instance admin.
            </>
          ) : (
            <>
              You can see rules for your projects, but adding or changing one needs editor access on that project.
              Ask a project admin to make you an editor.
            </>
          )}
        </Callout>
      )}
      {adminOnlyServer ? (
        <AdminOnlyState
          project={adding?.project || editable[0] || ""}
          service={adding?.service ?? ""}
          kind={adding?.kind}
        />
      ) : list.isPending ? (
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
                manageable={canManageAlertRule(access, r)}
                deliveryFailing={deliveryFailing}
                deleting={del.isPending && del.variables === r.id}
                onDelete={() => del.mutate(r.id)}
                onToggle={(on) => toggle.mutate({ id: r.id, on })}
              />
            ))}
          </ul>
        </SettingsSection>
      )}

      {adding && !adminOnlyServer && (
        <AddRuleDialog
          initial={adding}
          access={access}
          projects={editable}
          projectsLoading={projects.isPending}
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

// AdminOnlyState: a server that keeps alert rules admin-only. Members
// get the request text to send an admin instead of a form that 403s.
function AdminOnlyState({ project, service, kind }: { project: string; service: string; kind?: AlertKind }) {
  const text = alertRequestText({ project: project || undefined, service: service || undefined, kind });
  return (
    <SettingsSection
      title="Ask an admin to add a rule"
      description="This kuso server lets only instance admins manage alert rules. Send an admin this request; they add it under Settings → Alert rules."
    >
      <div className="relative rounded-md border border-[var(--border-subtle)] bg-[var(--bg-tertiary)] p-3 pr-10">
        <pre className="font-sans whitespace-pre-wrap break-words text-xs leading-relaxed text-[var(--text-primary)]">{text}</pre>
        <div className="absolute right-2 top-2">
          <CopyButton value={text} label="Copy request" toastMessage="Request copied" />
        </div>
      </div>
    </SettingsSection>
  );
}

function RuleRow({
  rule,
  manageable,
  deliveryFailing,
  deleting,
  onDelete,
  onToggle,
}: {
  rule: AlertRule;
  manageable: boolean;
  deliveryFailing: boolean;
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
          <span
            className="rounded-sm bg-[var(--bg-tertiary)] px-1.5 py-0.5 text-xs text-[var(--text-secondary)]"
            title={rule.kind}
          >
            {alertKindLabel(rule.kind)}
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
            {deliveryFailing && (
              <>
                {" · "}
                <Link href="/settings/notifications" className="text-[var(--error)] underline underline-offset-2">
                  notification delivery is failing
                </Link>
              </>
            )}
          </p>
        )}
        {!manageable && (
          <p className="mt-0.5 flex items-center gap-1 text-xs text-[var(--text-tertiary)]">
            <Lock aria-hidden className="h-3 w-3" />
            View only · changing it needs editor access on {rule.project}
          </p>
        )}
      </div>
      <div className="flex w-full items-center justify-end gap-3 sm:w-auto">
        <span className="flex items-center gap-2 text-xs text-[var(--text-secondary)]">
          <span aria-hidden>{rule.enabled ? "Enabled" : "Disabled"}</span>
          <Switch
            on={rule.enabled}
            onChange={onToggle}
            size="sm"
            label={`Evaluate ${rule.name}`}
            disabled={!manageable}
          />
        </span>
        {manageable && (
          <Button
            variant="ghost"
            size="icon-sm"
            onClick={() => setConfirming(true)}
            aria-label={`Delete ${rule.name}`}
            className="text-[var(--text-tertiary)] hover:text-[var(--error)]"
          >
            <Trash2 className="h-3.5 w-3.5" />
          </Button>
        )}
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
  access,
  projects,
  projectsLoading,
  onClose,
  onCreated,
}: {
  initial: RuleFormState;
  access: AlertAccessSession;
  /** Projects the caller may scope a rule to. */
  projects: string[];
  projectsLoading: boolean;
  onClose: () => void;
  onCreated: () => void;
}) {
  const isAdmin = access.adminAll;
  const kinds = alertKindsFor(access);
  const [initialForm] = useState<RuleFormState>(() => {
    // A deep link can name a project or kind this member can't use; drop
    // it rather than open a form that can only fail.
    if (isAdmin) return initial;
    const kind = kinds.includes(initial.kind) ? initial.kind : "http_5xx_rate";
    const base = kind === initial.kind ? initial : { ...emptyRuleForm(kind), name: initial.name };
    return projects.includes(base.project) ? base : { ...base, project: "", service: "", env: "" };
  });
  const [form, setForm] = useState<RuleFormState>(initialForm);
  const dirty = JSON.stringify(form) !== JSON.stringify(initialForm);
  const meta = ALERT_KINDS[form.kind];
  const set = <K extends keyof RuleFormState>(k: K, v: RuleFormState[K]) => setForm((f) => ({ ...f, [k]: v }));
  const built = buildCreateBody(form, { projectRequired: !isAdmin });
  const services = useServices(form.project);
  const serviceNames = useMemo(
    () =>
      (services.data ?? [])
        .map((s) => serviceShortName(form.project, s.metadata.name))
        .sort((a, b) => a.localeCompare(b)),
    [services.data, form.project],
  );
  // Keep a prefilled value selectable even if the list doesn't have it
  // (stale deep link, list still loading).
  const projectOptions = form.project && !projects.includes(form.project) ? [form.project, ...projects] : projects;
  const serviceOptions =
    form.service && !serviceNames.includes(form.service) ? [form.service, ...serviceNames] : serviceNames;
  const envs = useEnvironments(form.project);
  // Env groups (production, staging, preview-pr-N…) of the picked service,
  // or of the whole project when the rule covers every service.
  const envNames = useMemo(() => {
    const fqn = form.service ? `${form.project}-${form.service}` : "";
    const names = (envs.data ?? []).filter((e) => !fqn || e.spec.service === fqn).map((e) => envGroupName(e));
    return [...new Set(names)].sort((a, b) => (a === "production" ? -1 : b === "production" ? 1 : a.localeCompare(b)));
  }, [envs.data, form.project, form.service]);
  const envOptions = form.env && !envNames.includes(form.env) ? [form.env, ...envNames] : envNames;
  const setProject = (project: string) => setForm((f) => ({ ...f, project, service: "", env: "" }));
  const noProjectReason = "Pick a project first";

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
                {KIND_GROUPS.filter((g) => kinds.some((k) => ALERT_KINDS[k].group === g)).map((g) => (
                  <optgroup key={g} label={g}>
                    {kinds
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
              <Field
                label={isAdmin ? "Project" : "Project (required)"}
                hint={!isAdmin && projects.length === 0 && !projectsLoading ? "No project you can edit" : undefined}
              >
                <SettingsSelect
                  value={form.project}
                  onChange={(e) => setProject(e.target.value)}
                  placeholder={isAdmin ? undefined : projectsLoading ? "Loading projects…" : "Pick a project"}
                >
                  {isAdmin && <option value="">All projects</option>}
                  {projectOptions.map((p) => (
                    <option key={p} value={p}>
                      {p}
                    </option>
                  ))}
                </SettingsSelect>
              </Field>
              <Field
                label="Service"
                hint={
                  !form.project
                    ? noProjectReason
                    : services.isError
                      ? "Couldn't load services — the rule covers all of them"
                      : undefined
                }
              >
                <SettingsSelect
                  value={form.service}
                  onChange={(e) => set("service", e.target.value)}
                  disabled={!form.project || services.isError}
                  title={!form.project ? noProjectReason : undefined}
                >
                  <option value="">{services.isPending && form.project ? "Loading services…" : "All services"}</option>
                  {serviceOptions.map((s) => (
                    <option key={s} value={s}>
                      {s}
                    </option>
                  ))}
                </SettingsSelect>
              </Field>
              {meta.scope === "env" && (
                <Field label="Environment" hint={!form.project ? noProjectReason : undefined}>
                  <SettingsSelect
                    value={form.env}
                    onChange={(e) => set("env", e.target.value)}
                    disabled={!form.project}
                    title={!form.project ? noProjectReason : undefined}
                  >
                    <option value="">{envs.isPending && form.project ? "Loading environments…" : "All environments"}</option>
                    {envOptions.map((n) => (
                      <option key={n} value={n}>
                        {n}
                      </option>
                    ))}
                  </SettingsSelect>
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
        </div>
        <footer className="flex flex-wrap items-center justify-end gap-2 border-t border-[var(--border-subtle)] px-4 py-3">
          {!built.ok && (
            <p id="alert-rule-blocked" role="status" className="mr-auto text-xs text-[var(--text-secondary)]">
              {built.error}
            </p>
          )}
          <Button size="sm" variant="ghost" onClick={onClose} disabled={create.isPending}>
            Cancel
          </Button>
          <Button
            size="sm"
            disabled={!built.ok || create.isPending}
            aria-describedby={!built.ok ? "alert-rule-blocked" : undefined}
            onClick={() => create.mutate()}
          >
            {create.isPending ? "Creating…" : "Create rule"}
          </Button>
        </footer>
      </DialogContent>
    </Dialog>
  );
}

function Field({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <label className="block space-y-1.5">
      <span className="block text-sm font-medium text-[var(--text-primary)]">{label}</span>
      {children}
      {hint && <span className="block text-xs text-[var(--text-tertiary)]">{hint}</span>}
    </label>
  );
}
