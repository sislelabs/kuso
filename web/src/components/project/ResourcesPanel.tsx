"use client";

import { useMemo } from "react";
import {
  useAddons,
  useEnvironments,
  useServices,
} from "@/features/projects";
import { LoadingState } from "@/components/ui/loading-state";
import { Database, Server, Globe, AlertCircle, CheckCircle2 } from "lucide-react";
import { cn } from "@/lib/utils";

// ResourcesPanel renders a flat, scannable list of every CR that
// belongs to a project — services, environments, addons — plus their
// helm-operator status when we can derive it from .status. Used as a
// debug surface so users don't have to kubectl-spelunk to answer
// "what's the operator complaining about?".
//
// Why a panel and not a route: the ServiceOverlay already has
// Deployments / Variables / etc. tabs; a project-level route would
// fragment navigation. Putting this on the canvas as an opt-in
// pop-out keeps it discoverable without competing with the canvas
// itself for the default surface.
//
// We don't poll separately — the existing services / envs / addons
// queries are already hot via the canvas. Read-only.
export function ResourcesPanel({ project }: { project: string }) {
  const services = useServices(project);
  const envs = useEnvironments(project);
  const addons = useAddons(project);
  const loading = services.isPending || envs.isPending || addons.isPending;
  const rows = useMemo(() => {
    const out: ResourceRow[] = [];
    (services.data ?? []).forEach((s) => {
      out.push({
        kind: "Service",
        name: s.metadata.name,
        ready: undefined, // services don't carry ready status; envs do
        message: s.spec.runtime ? `runtime=${s.spec.runtime}` : undefined,
        icon: Server,
      });
    });
    (envs.data ?? []).forEach((e) => {
      const status = (e.status ?? {}) as { ready?: boolean; phase?: string; helmError?: string };
      out.push({
        kind: "Environment",
        name: e.metadata.name,
        ready: status.ready,
        message: status.helmError || status.phase || undefined,
        icon: Globe,
      });
    });
    (addons.data ?? []).forEach((a) => {
      const status = (a.status ?? {}) as { ready?: boolean };
      out.push({
        kind: "Addon",
        name: a.metadata.name,
        ready: status.ready,
        message: a.spec.kind ? `kind=${a.spec.kind}` : undefined,
        icon: Database,
      });
    });
    return out.sort((a, b) =>
      a.kind === b.kind ? a.name.localeCompare(b.name) : a.kind.localeCompare(b.kind)
    );
  }, [services.data, envs.data, addons.data]);
  if (loading) {
    return <LoadingState kind="list" />;
  }
  // A failed fetch must not masquerade as "no resources yet" (which reads
  // as an empty project). Surface the error with a retry instead.
  const anyError = services.isError || envs.isError || addons.isError;
  if (anyError) {
    const e = services.error || envs.error || addons.error;
    const err = e instanceof Error ? e.message : "request failed";
    return (
      <div className="rounded-md border border-red-500/30 bg-red-500/5 p-4 text-[12px]">
        <p className="text-red-400">Couldn&apos;t load project resources: {err}</p>
        <button
          type="button"
          onClick={() => {
            services.refetch();
            envs.refetch();
            addons.refetch();
          }}
          className="mt-2 font-mono text-[11px] text-[var(--accent)] hover:underline"
        >
          Retry
        </button>
      </div>
    );
  }
  return (
    <div className="space-y-1">
      <header className="mb-2 flex items-baseline justify-between">
        <h3 className="font-heading text-sm font-semibold tracking-tight">Resources</h3>
        <span className="font-mono text-[10px] text-[var(--text-tertiary)]">
          {rows.length} CR{rows.length === 1 ? "" : "s"}
        </span>
      </header>
      <ul className="divide-y divide-[var(--border-subtle)] rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
        {rows.length === 0 && (
          <li className="px-3 py-4 text-center font-mono text-[11px] text-[var(--text-tertiary)]">
            no resources yet
          </li>
        )}
        {rows.map((r) => (
          <li
            key={`${r.kind}/${r.name}`}
            className="flex items-center gap-3 px-3 py-2"
          >
            <r.icon className="h-3.5 w-3.5 shrink-0 text-[var(--text-tertiary)]" />
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2 truncate font-mono text-[11px] text-[var(--text-primary)]">
                {r.name}
                <span className="font-mono text-[9px] uppercase tracking-widest text-[var(--text-tertiary)]">
                  {r.kind}
                </span>
              </div>
              {r.message && (
                <div className="truncate font-mono text-[10px] text-[var(--text-tertiary)]">
                  {r.message}
                </div>
              )}
            </div>
            <ReadyBadge ready={r.ready} />
          </li>
        ))}
      </ul>
    </div>
  );
}

interface ResourceRow {
  kind: string;
  name: string;
  ready?: boolean;
  message?: string;
  icon: React.ComponentType<{ className?: string }>;
}

function ReadyBadge({ ready }: { ready?: boolean }) {
  if (ready === undefined) {
    return (
      <span className="font-mono text-[9px] uppercase tracking-widest text-[var(--text-tertiary)]">
        —
      </span>
    );
  }
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 font-mono text-[9px] uppercase tracking-widest",
        ready ? "text-emerald-400" : "text-amber-400"
      )}
    >
      {ready ? <CheckCircle2 className="h-3 w-3" /> : <AlertCircle className="h-3 w-3" />}
      {ready ? "ready" : "not ready"}
    </span>
  );
}
