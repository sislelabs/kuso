"use client";

// /settings/health — "is everything I run OK?"
//
// Top: the app-level rollup (AppRollupSection) — services, uptime,
// errors and addons across every project, under one verdict line.
// Below it, the server's reconcile scan (GET /api/health/reconcile):
// every Kuso CR + its rendered helm release walked for anything that
// isn't cleanly reconciling — failed helm installs, stuck addons,
// orphaned envs. Issues group severity-first (critical → warning →
// info). Each row expands to the raw helm error + fix prose.
//
//   safe + action  → one-click "Fix" (POST /remediate) behind a
//                    ConfirmDialog, toast the result, row refreshes.
//   !safe          → copy-paste runbook command instead — these need
//                    human judgment, not an auto-fix.

import { useMemo, useState } from "react";
import { toast } from "sonner";
import { toastError } from "@/lib/errors";
import {
  HeartPulse,
  AlertTriangle,
  AlertCircle,
  Info,
  ChevronDown,
  ChevronRight,
  Wrench,
  Copy,
  Check,
  Trash2,
} from "lucide-react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import { Skeleton } from "@/components/ui/skeleton";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import {
  reconcileHealthQueryKey,
  useReconcileHealth,
  useRemediate,
  type ReconcileIssue,
} from "@/features/health";
import { cn } from "@/lib/utils";
import { AppRollupSection } from "@/components/health/AppRollupSection";
import { KubeEventsSection } from "@/components/health/KubeEventsSection";
import { SettingsPage } from "@/components/settings/SettingsPage";
import { SettingsNotice, SettingsSection } from "@/components/settings/SettingsSection";

type Severity = ReconcileIssue["severity"];

const SEVERITY_ORDER: Severity[] = ["critical", "warning", "info"];

// Leftovers of deleted addons. Listed on their own with a Delete button
// instead of among reconcile problems.
const ORPHAN_KINDS: Record<string, string> = {
  orphan_conn_secret: "credentials",
  orphan_addon_pvc: "data",
};
const isOrphan = (i: ReconcileIssue) => !!i.kind && i.kind in ORPHAN_KINDS;

function reconcileCounts(all: ReconcileIssue[]): { issues: number; critical: number } {
  const issues = all.filter((i) => !isOrphan(i));
  return { issues: issues.length, critical: issues.filter((i) => i.severity === "critical").length };
}

const SEVERITY_META: Record<
  Severity,
  { label: string; icon: React.ComponentType<{ className?: string }>; pill: string; border: string }
> = {
  critical: {
    label: "Critical",
    icon: AlertCircle,
    pill: "bg-[var(--error-subtle)] text-[var(--error)] border-[var(--error)]/30",
    border: "border-[var(--error)]/30",
  },
  warning: {
    label: "Warning",
    icon: AlertTriangle,
    pill: "bg-[var(--warning-subtle)] text-[var(--warning)] border-[var(--warning)]/30",
    border: "border-[var(--warning)]/30",
  },
  info: {
    label: "Info",
    icon: Info,
    pill: "bg-[var(--info-subtle)] text-[var(--info)] border-[var(--info)]/30",
    border: "border-[var(--info)]/30",
  },
};

function SeverityPill({ severity, count }: { severity: Severity; count?: number }) {
  const m = SEVERITY_META[severity];
  const Icon = m.icon;
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs font-medium",
        m.pill
      )}
    >
      <Icon className="h-3 w-3" />
      {count !== undefined ? `${count} ` : ""}
      {m.label}
    </span>
  );
}

export default function HealthPage() {
  const { data, isPending, isError, error } = useReconcileHealth();

  return (
    <SettingsPage
      title="Health"
      description="Whether everything you run is up, across every project. Below that, the reconcile scan of every kuso resource and its helm release, and recent Kubernetes events."
    >
      <AppRollupSection reconcile={data ? reconcileCounts(data.issues ?? []) : undefined} />

      {isPending ? (
        <div className="space-y-3">
          <Skeleton className="h-20 w-full" />
          <Skeleton className="h-16 w-full" />
        </div>
      ) : isError ? (
        <SettingsNotice tone="error" title="Couldn't load reconcile health">
          {error instanceof Error ? error.message : "Unknown error"}
        </SettingsNotice>
      ) : !data ? null : (
        <HealthBody report={data} />
      )}

      <KubeEventsSection />
    </SettingsPage>
  );
}

function HealthBody({ report }: { report: NonNullable<ReturnType<typeof useReconcileHealth>["data"]> }) {
  const { grouped, issues, orphans } = useMemo(() => {
    const by: Record<Severity, ReconcileIssue[]> = { critical: [], warning: [], info: [] };
    const rest: ReconcileIssue[] = [];
    const orph: ReconcileIssue[] = [];
    for (const issue of report.issues ?? []) {
      if (isOrphan(issue)) {
        orph.push(issue);
        continue;
      }
      rest.push(issue);
      const sev = SEVERITY_META[issue.severity] ? issue.severity : "info";
      by[sev].push(issue);
    }
    return { grouped: by, issues: rest, orphans: orph };
  }, [report.issues]);

  const hasIssues = issues.length > 0;

  return (
    <div className="space-y-6">
      {/* Neutral on purpose: the verdict above is the page's one success state. */}
      <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-4 sm:p-5">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex items-center gap-3">
            <span
              aria-hidden
              className={cn(
                "inline-flex size-8 shrink-0 items-center justify-center rounded-md",
                hasIssues
                  ? "bg-[var(--warning-subtle)] text-[var(--warning)]"
                  : "bg-[var(--bg-tertiary)] text-[var(--text-tertiary)]",
              )}
            >
              <HeartPulse className="size-4" />
            </span>
            <div>
              <h2 className="text-base font-semibold tracking-tight text-[var(--text-primary)]">Reconcile</h2>
              <div className="mt-0.5 text-sm text-[var(--text-secondary)]">
                {hasIssues
                  ? `${issues.length} issue${issues.length === 1 ? "" : "s"} · ${report.healthy} of ${report.scanned} resources reconciling cleanly`
                  : `All ${report.scanned} resources reconciling cleanly`}
              </div>
            </div>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            {SEVERITY_ORDER.map((sev) =>
              grouped[sev].length > 0 ? (
                <SeverityPill key={sev} severity={sev} count={grouped[sev].length} />
              ) : null,
            )}
          </div>
        </div>
      </section>

      {(report.skippedNamespaces ?? []).length > 0 && (
        <SettingsNotice tone="warning" title={`Couldn't scan ${report.skippedNamespaces!.length} namespace${report.skippedNamespaces!.length === 1 ? "" : "s"}`}>
          <span className="font-mono text-xs">{report.skippedNamespaces!.join(", ")}</span>. Resources there
          aren&apos;t counted above, so this report may be missing problems.
        </SettingsNotice>
      )}

      {hasIssues && (
        <div className="space-y-5">
          {SEVERITY_ORDER.map((sev) => {
            const issues = grouped[sev];
            if (issues.length === 0) return null;
            return (
              <section key={sev}>
                <header className="mb-2 flex items-center gap-2">
                  <SeverityPill severity={sev} />
                  <span className="text-xs text-[var(--text-secondary)]">
                    {issues.length} issue{issues.length === 1 ? "" : "s"}
                  </span>
                </header>
                <ul className="space-y-2">
                  {issues.map((issue, i) => (
                    <IssueRow key={`${issue.resource}:${issue.type ?? ""}:${i}`} issue={issue} />
                  ))}
                </ul>
              </section>
            );
          })}
        </div>
      )}

      {orphans.length > 0 && <OrphansSection orphans={orphans} />}
    </div>
  );
}

function OrphansSection({ orphans }: { orphans: ReconcileIssue[] }) {
  const qc = useQueryClient();
  const [target, setTarget] = useState<ReconcileIssue | null>(null);
  const [deleted, setDeleted] = useState<Set<string>>(new Set());
  const keyOf = (o: ReconcileIssue) => `${o.namespace ?? ""}/${o.resource}`;
  const del = useMutation({
    mutationFn: (o: ReconcileIssue) => {
      const q = new URLSearchParams({
        kind: o.kind ?? "",
        namespace: o.namespace ?? "",
        name: o.resource,
      });
      return api(`/api/admin/orphans?${q.toString()}`, { method: "DELETE" });
    },
    onSuccess: (_res, o) => {
      toast.success(`Deleted ${o.resource}`);
      // The report is cached server-side for 30s; hide the row now.
      setDeleted((prev) => new Set(prev).add(keyOf(o)));
      setTarget(null);
      qc.invalidateQueries({ queryKey: reconcileHealthQueryKey });
    },
    onError: (e) => toastError(e, "Delete failed"),
  });
  const visible = orphans.filter((o) => !deleted.has(keyOf(o)));
  if (visible.length === 0) return null;
  return (
    <SettingsSection title="Orphans" description="Credentials and volumes left over from deleted addons." flush>
      <ul className="divide-y divide-[var(--border-subtle)]">
        {visible.map((o) => (
          <li key={keyOf(o)} className="flex flex-wrap items-center gap-2 px-4 py-2.5 text-sm sm:px-5" title={o.summary}>
            <span className="min-w-0 flex-1 truncate font-mono">{o.resource}</span>
            {o.project && (
              <span className="text-xs text-[var(--text-secondary)]">{o.project}</span>
            )}
            <span className="rounded-sm bg-[var(--bg-tertiary)] px-1.5 py-0.5 text-xs text-[var(--text-secondary)]">
              {ORPHAN_KINDS[o.kind ?? ""]}
            </span>
            <Button
              size="sm"
              variant="ghost"
              className="h-6 px-1.5 text-[var(--error)]"
              onClick={() => setTarget(o)}
              aria-label={`Delete ${o.resource}`}
            >
              <Trash2 className="h-3.5 w-3.5" />
            </Button>
          </li>
        ))}
      </ul>
      <ConfirmDialog
        open={!!target}
        title={`Delete ${target?.resource ?? ""}?`}
        body={
          <p>
            {target?.kind === "orphan_addon_pvc"
              ? "The data in this volume is deleted. No undo."
              : "These credentials are deleted. No undo."}
          </p>
        }
        typeToConfirm={target?.resource}
        confirmLabel="Delete"
        pending={del.isPending}
        onConfirm={() => target && del.mutate(target)}
        onCancel={() => setTarget(null)}
      />
    </SettingsSection>
  );
}

function IssueRow({ issue }: { issue: ReconcileIssue }) {
  const [open, setOpen] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const remediate = useRemediate();
  const m = SEVERITY_META[issue.severity] ?? SEVERITY_META.info;
  const canAutoFix = !!issue.safe && !!issue.action;

  const onConfirmFix = () => {
    remediate.mutate(
      { resource: issue.resource, action: issue.action },
      {
        onSuccess: (res) => {
          setConfirming(false);
          if (res.applied) {
            toast.success(res.message || `Applied ${res.action} to ${res.resource}`);
          } else {
            toast.warning(res.message || `${res.action} did not apply`);
          }
        },
        onError: (e) => {
          setConfirming(false);
          toastError(e, "Remediation failed");
        },
      }
    );
  };

  return (
    <li
      className={cn(
        "overflow-hidden rounded-md border bg-[var(--bg-secondary)]",
        m.border
      )}
    >
      <div className="flex items-center gap-2 px-3 py-2.5">
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          className="flex flex-1 items-start gap-2 text-left"
        >
          {open ? (
            <ChevronDown className="mt-0.5 h-4 w-4 shrink-0 text-[var(--text-tertiary)]" />
          ) : (
            <ChevronRight className="mt-0.5 h-4 w-4 shrink-0 text-[var(--text-tertiary)]" />
          )}
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
              <span className="truncate font-mono text-sm font-medium text-[var(--text-primary)]">
                {issue.resource}
              </span>
              {issue.kind && (
                <span className="rounded-sm bg-[var(--bg-tertiary)] px-1.5 py-0.5 font-mono text-xs text-[var(--text-secondary)]">
                  {issue.kind}
                  {issue.addonKind ? ` · ${issue.addonKind}` : ""}
                </span>
              )}
              {issue.project && (
                <span className="text-xs text-[var(--text-secondary)]">{issue.project}</span>
              )}
            </div>
            <div className="mt-0.5 text-sm text-[var(--text-secondary)] sm:truncate">
              {issue.summary}
            </div>
          </div>
        </button>
        {canAutoFix && (
          <Button
            size="sm"
            variant="neutral"
            disabled={remediate.isPending}
            onClick={() => setConfirming(true)}
            className="shrink-0"
          >
            <Wrench className="h-3.5 w-3.5" />
            Fix
          </Button>
        )}
      </div>

      {open && (
        <div className="space-y-3 border-t border-[var(--border-subtle)] bg-[var(--bg-primary)] px-3 py-3">
          {issue.detail && (
            <div>
              <div className="mb-1 text-xs font-medium text-[var(--text-secondary)]">
                Detail
              </div>
              <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-words rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-2 font-mono text-xs leading-snug text-[var(--text-secondary)]">
                {issue.detail}
              </pre>
            </div>
          )}
          {issue.fix && (
            <div>
              <div className="mb-1 text-xs font-medium text-[var(--text-secondary)]">
                {canAutoFix ? "What the fix does" : "Fix"}
              </div>
              <p className="text-sm leading-relaxed text-[var(--text-secondary)]">{issue.fix}</p>
            </div>
          )}
          {!canAutoFix && issue.runbookCmd && (
            <div>
              <div className="mb-1 text-xs font-medium text-[var(--text-secondary)]">
                Runbook (needs human judgment)
              </div>
              <CopyableCommand command={issue.runbookCmd} />
            </div>
          )}
        </div>
      )}

      <ConfirmDialog
        open={confirming}
        title="Apply remediation?"
        destructive={false}
        confirmLabel="Apply fix"
        pending={remediate.isPending}
        onConfirm={onConfirmFix}
        onCancel={() => setConfirming(false)}
        body={
          <div className="space-y-2">
            <p>
              Run <span className="font-mono text-[var(--text-primary)]">{issue.action}</span> on{" "}
              <span className="font-mono text-[var(--text-primary)]">{issue.resource}</span>?
            </p>
            {issue.fix && <p className="text-sm text-[var(--text-secondary)]">{issue.fix}</p>}
          </div>
        }
      />
    </li>
  );
}

// CopyableCommand renders a runbook command in a mono block with a
// copy button. Used for !safe issues — the operator runs it by hand
// after eyeballing it.
function CopyableCommand({ command }: { command: string }) {
  const [copied, setCopied] = useState(false);
  const onCopy = async () => {
    try {
      await navigator.clipboard.writeText(command);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      toast.error("clipboard unavailable");
    }
  };
  return (
    <div className="flex items-stretch gap-2">
      <pre className="min-w-0 flex-1 overflow-auto whitespace-pre-wrap break-words rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-2 font-mono text-xs leading-snug text-[var(--text-secondary)]">
        {command}
      </pre>
      <button
        type="button"
        onClick={onCopy}
        aria-label="Copy command"
        className="inline-flex shrink-0 items-center justify-center rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
      >
        {copied ? <Check className="h-3.5 w-3.5 text-[var(--success)]" /> : <Copy className="h-3.5 w-3.5" />}
      </button>
    </div>
  );
}
