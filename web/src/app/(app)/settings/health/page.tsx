"use client";

// /settings/health — reconcile-health dashboard.
//
// Surfaces the server's reconcile scan (GET /api/health/reconcile):
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
import {
  ShieldCheck,
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

type Severity = ReconcileIssue["severity"];

const SEVERITY_ORDER: Severity[] = ["critical", "warning", "info"];

// Leftovers of deleted addons. Listed on their own with a Delete button
// instead of among reconcile problems.
const ORPHAN_KINDS: Record<string, string> = {
  orphan_conn_secret: "credentials",
  orphan_addon_pvc: "data",
};
const isOrphan = (i: ReconcileIssue) => !!i.kind && i.kind in ORPHAN_KINDS;

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
        "inline-flex items-center gap-1 rounded-full border px-2 py-0.5 font-mono text-[10px] font-semibold uppercase tracking-widest",
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
    <div className="mx-auto max-w-5xl p-6 lg:p-8">
      <header className="mb-6">
        <h1 className="font-heading text-2xl font-semibold tracking-tight">Reconcile health</h1>
        <p className="mt-1 text-sm text-[var(--text-secondary)]">
          Every Kuso resource and its rendered helm release, scanned for anything that isn&apos;t
          reconciling cleanly. Safe fixes apply with one click; the rest hand you a runbook command.
        </p>
      </header>

      {isPending ? (
        <div className="space-y-3">
          <Skeleton className="h-20 w-full" />
          <Skeleton className="h-16 w-full" />
          <Skeleton className="h-16 w-full" />
        </div>
      ) : isError ? (
        <p className="rounded-md border border-[var(--error)]/30 bg-[var(--error-subtle)] p-4 text-sm text-[var(--error)]">
          Couldn&apos;t load reconcile health:{" "}
          {error instanceof Error ? error.message : "unknown error"}
        </p>
      ) : !data ? null : (
        <HealthBody report={data} />
      )}
    </div>
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
      {/* Summary header. */}
      <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-4">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex items-center gap-3">
            <span
              className={cn(
                "inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-md",
                hasIssues
                  ? "bg-[var(--warning-subtle)] text-[var(--warning)]"
                  : "bg-emerald-500/10 text-[var(--success)]"
              )}
            >
              <ShieldCheck className="h-5 w-5" />
            </span>
            <div>
              <div className="text-sm font-semibold tracking-tight text-[var(--text-primary)]">
                {report.healthy} healthy{" "}
                <span className="text-[var(--text-tertiary)]">/ {report.scanned} scanned</span>
              </div>
              <div className="mt-0.5 font-mono text-[11px] text-[var(--text-tertiary)]">
                {hasIssues
                  ? `${issues.length} issue${issues.length === 1 ? "" : "s"} need attention`
                  : "all resources reconciling cleanly"}
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

      {!hasIssues ? (
        <div className="rounded-md border border-emerald-500/30 bg-emerald-500/5 p-8 text-center">
          <ShieldCheck className="mx-auto h-8 w-8 text-[var(--success)]" />
          <p className="mt-3 text-sm font-medium text-[var(--success)]">
            ✓ All {report.scanned} resources reconciling cleanly.
          </p>
        </div>
      ) : (
        <div className="space-y-5">
          {SEVERITY_ORDER.map((sev) => {
            const issues = grouped[sev];
            if (issues.length === 0) return null;
            return (
              <section key={sev}>
                <header className="mb-2 flex items-center gap-2">
                  <SeverityPill severity={sev} />
                  <span className="font-mono text-[10px] text-[var(--text-tertiary)]">
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
    onError: (e) => toast.error(e instanceof Error ? e.message : "Delete failed"),
  });
  const visible = orphans.filter((o) => !deleted.has(keyOf(o)));
  if (visible.length === 0) return null;
  return (
    <section>
      <header className="mb-2 flex items-center gap-2">
        <h2 className="text-sm font-semibold">Orphans</h2>
        <span className="font-mono text-[10px] text-[var(--text-tertiary)]">
          left over from deleted addons
        </span>
      </header>
      <ul className="divide-y divide-[var(--border-subtle)] rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
        {visible.map((o) => (
          <li key={keyOf(o)} className="flex items-center gap-2 px-3 py-2 text-[12px]" title={o.summary}>
            <span className="min-w-0 flex-1 truncate font-mono">{o.resource}</span>
            {o.project && (
              <span className="font-mono text-[10px] text-[var(--text-tertiary)]">{o.project}</span>
            )}
            <span className="rounded bg-[var(--bg-tertiary)] px-1.5 py-0.5 font-mono text-[10px] text-[var(--text-tertiary)]">
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
    </section>
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
          toast.error(e instanceof Error ? e.message : "Remediation failed");
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
              <span className="truncate font-mono text-[12px] font-medium text-[var(--text-primary)]">
                {issue.resource}
              </span>
              {issue.kind && (
                <span className="rounded bg-[var(--bg-tertiary)] px-1.5 py-0.5 font-mono text-[9px] uppercase tracking-widest text-[var(--text-tertiary)]">
                  {issue.kind}
                  {issue.addonKind ? ` · ${issue.addonKind}` : ""}
                </span>
              )}
              {issue.project && (
                <span className="font-mono text-[10px] text-[var(--text-tertiary)]">
                  {issue.project}
                </span>
              )}
            </div>
            <div className="mt-0.5 truncate text-[12px] text-[var(--text-secondary)]">
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
              <div className="mb-1 font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
                detail
              </div>
              <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-words rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-2 font-mono text-[11px] leading-snug text-[var(--text-secondary)]">
                {issue.detail}
              </pre>
            </div>
          )}
          {issue.fix && (
            <div>
              <div className="mb-1 font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
                {canAutoFix ? "what the fix does" : "fix"}
              </div>
              <p className="text-[12px] leading-relaxed text-[var(--text-secondary)]">{issue.fix}</p>
            </div>
          )}
          {!canAutoFix && issue.runbookCmd && (
            <div>
              <div className="mb-1 font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
                runbook — needs human judgment
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
            {issue.fix && <p className="text-[12px] text-[var(--text-tertiary)]">{issue.fix}</p>}
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
      <pre className="min-w-0 flex-1 overflow-auto whitespace-pre-wrap break-words rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-2 font-mono text-[11px] leading-snug text-[var(--text-secondary)]">
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
