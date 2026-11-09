"use client";

import { Suspense, useEffect, useMemo, useState } from "react";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Bell, Lock, Pencil, Plus, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import {
  listAlerts,
  deleteAlert,
  enableAlert,
  disableAlert,
  alertKindLabel,
  describeRule,
  emptyRuleForm,
  prefillRuleForm,
  ruleStatus,
  ruleToForm,
  alertRequestText,
  canManageAlertRule,
  editableAlertProjects,
  type AlertAccessSession,
  type AlertKind,
  type AlertRule,
  type RuleFormState,
  type RuleStatus,
} from "@/features/alerts";
import { toastError } from "@/lib/errors";
import { Switch } from "@/components/ui/switch";
import { relativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import { EmptyState } from "@/components/shared/EmptyState";
import Link from "next/link";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { AlertRuleDialog } from "./AlertRuleDialog";
import { QueryErrorState } from "@/components/shared/QueryErrorState";
import { SettingsPage } from "@/components/settings/SettingsPage";
import { SettingsSection } from "@/components/settings/SettingsSection";
import { Callout } from "@/components/ui/callout";
import { CopyButton } from "@/components/ui/copy-button";
import { useSession } from "@/features/auth";
import { useProjects } from "@/features/projects";
import { api, ApiError } from "@/lib/api-client";

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
  const [editing, setEditing] = useState<AlertRule | null>(null);
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
          description="Click New rule to watch crash loops, failed deploys, uptime, 5xx rates, latency, certificates, DNS, logs or nodes."
          className="p-8"
        />
      ) : (
        <SettingsSection
          title="Rules"
          description={`${(list.data ?? []).length} ${(list.data ?? []).length === 1 ? "rule" : "rules"}. Disabled rules aren't evaluated.`}
          flush
        >
          <StatusLegend />
          <ul className="divide-y divide-[var(--border-subtle)]">
            {(list.data ?? []).map((r) => (
              <RuleRow
                key={r.id}
                rule={r}
                manageable={canManageAlertRule(access, r)}
                deliveryFailing={deliveryFailing}
                deleting={del.isPending && del.variables === r.id}
                onDelete={() => del.mutate(r.id)}
                onEdit={() => setEditing(r)}
                onToggle={(on) => toggle.mutate({ id: r.id, on })}
              />
            ))}
          </ul>
        </SettingsSection>
      )}

      {adding && !adminOnlyServer && (
        <AlertRuleDialog
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
      {editing && (
        <AlertRuleDialog
          key={editing.id}
          initial={ruleToForm(editing)}
          editing={editing}
          access={access}
          projects={editable}
          projectsLoading={projects.isPending}
          onClose={() => setEditing(null)}
          onCreated={() => {
            setEditing(null);
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
  onEdit,
  onToggle,
}: {
  rule: AlertRule;
  manageable: boolean;
  deliveryFailing: boolean;
  deleting: boolean;
  onDelete: () => void;
  onEdit: () => void;
  onToggle: (on: boolean) => void;
}) {
  const [confirming, setConfirming] = useState(false);
  const detail = describeRule(rule);
  const status = ruleStatus(rule);
  return (
    <li className="flex flex-wrap items-start gap-x-3 gap-y-2 px-4 py-3 sm:flex-nowrap sm:px-5">
      <StatusDot status={status} className="mt-1.5" />
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
          <p className="truncate text-sm font-medium text-[var(--text-primary)]">{rule.name}</p>
          <span className="sr-only">Status: {STATUS_META[status].label}.</span>
          <span
            className="rounded-sm bg-[var(--bg-tertiary)] px-1.5 py-0.5 text-xs text-[var(--text-secondary)]"
            title={rule.kind}
          >
            {alertKindLabel(rule.kind)}
          </span>
          <span className="text-xs text-[var(--text-tertiary)]">{SEVERITY_LABEL[rule.severity]}</span>
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
          <p className={cn("mt-0.5 text-xs", status === "firing" ? "text-[var(--error)]" : "text-[var(--text-secondary)]")}>
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
          <Button variant="ghost" size="sm" onClick={onEdit} aria-label={`Edit ${rule.name}`}>
            <Pencil className="h-3.5 w-3.5" />
            Edit
          </Button>
        )}
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

const SEVERITY_LABEL: Record<AlertRule["severity"], string> = { info: "Info", warn: "Warning", error: "Error" };

const STATUS_META: Record<RuleStatus, { label: string; dot: string; hint: string }> = {
  firing: { label: "Firing", dot: "bg-[var(--error)]", hint: "The condition holds right now" },
  ok: { label: "OK", dot: "bg-[var(--success)]", hint: "Has fired before; quiet now" },
  never: {
    label: "Never fired",
    dot: "border border-[var(--text-tertiary)] bg-transparent",
    hint: "Evaluated every minute, hasn't fired yet",
  },
  disabled: { label: "Off", dot: "bg-[var(--border-strong)]", hint: "Not evaluated" },
};

function StatusDot({ status, className }: { status: RuleStatus; className?: string }) {
  const m = STATUS_META[status];
  return (
    <span
      aria-hidden
      title={`${m.label}: ${m.hint}`}
      className={cn("inline-block h-2 w-2 shrink-0 rounded-full", m.dot, className)}
    />
  );
}

function StatusLegend() {
  return (
    <ul
      aria-label="Status legend"
      className="flex flex-wrap gap-x-4 gap-y-1 border-b border-[var(--border-subtle)] px-4 py-2 text-xs text-[var(--text-secondary)] sm:px-5"
    >
      {(Object.keys(STATUS_META) as RuleStatus[]).map((s) => (
        <li key={s} className="flex items-center gap-1.5" title={STATUS_META[s].hint}>
          <StatusDot status={s} />
          {STATUS_META[s].label}
        </li>
      ))}
    </ul>
  );
}
