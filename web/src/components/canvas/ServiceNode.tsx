"use client";

import { useState } from "react";
import { Handle, Position } from "@xyflow/react";
import { Check, Copy, ExternalLink } from "lucide-react";
import type { KusoEnvironment, KusoService } from "@/types/projects";
import { DeployStatusPill, type DeployStatus } from "@/components/service/DeployStatusPill";
import { SleepBadge } from "@/components/service/SleepBadge";
import { RuntimeIcon } from "@/components/service/RuntimeIcon";
import { cn, serviceShortName } from "@/lib/utils";
import { toast } from "sonner";

export interface ServiceNodeData extends Record<string, unknown> {
  project: string;
  service: KusoService;
  env?: KusoEnvironment;
  // Injected by ProjectCanvas — fires the right-click context menu.
  __onContext?: (e: React.MouseEvent) => void;
}

function statusFor(env?: KusoEnvironment): DeployStatus {
  if (!env) return "unknown";
  const phase = (env.status?.phase ?? "").toString().toLowerCase();
  if (phase === "building") return "building";
  if (phase === "deploying") return "deploying";
  if (env.status?.ready) return "active";
  if (phase === "failed" || phase === "error") return "failed";
  if (phase === "sleeping") return "sleeping";
  return "unknown";
}

interface Replicas {
  ready: number;
  max: number;
  cpuPct?: number;
}

function replicasFor(env?: KusoEnvironment): Replicas | null {
  const r = env?.status?.replicas as
    | { ready?: number; desired?: number; max?: number }
    | undefined;
  if (!r || (r.desired === undefined && r.ready === undefined && r.max === undefined)) {
    return null;
  }
  // Prefer max (autoscale ceiling) over desired so the badge reads
  // 1/5 even when only one pod is currently scheduled. Fall back to
  // desired when max isn't surfaced yet.
  const ceil = r.max ?? r.desired ?? 0;
  const cpu = (env?.status?.cpuPct as number | undefined) ?? undefined;
  return { ready: r.ready ?? 0, max: ceil, cpuPct: cpu };
}

export function ServiceNode({ data }: { data: ServiceNodeData }) {
  const status = statusFor(data.env);
  const url = data.env?.status?.url as string | undefined;
  const replicas = replicasFor(data.env);
  const shortName = serviceShortName(data.project, data.service.metadata.name);

  return (
    <div
      data-node-context
      onContextMenu={data.__onContext}
      className={cn(
        "group w-[280px] rounded-2xl border bg-[var(--bg-elevated)] p-3 transition-colors",
        "hover:border-[var(--border-strong)]",
        (status === "building" || status === "deploying") &&
          "border-[var(--accent)]/40 animate-pulse",
        status === "active" && "border-emerald-500/30",
        status === "failed" && "border-red-500/30",
        status === "sleeping" && "opacity-60 border-[var(--border-strong)]",
        !["building", "deploying", "active", "failed", "sleeping"].includes(status) &&
          "border-[var(--border-strong)]"
      )}
    >
      <Handle type="target" position={Position.Left} className="!bg-[var(--accent)]" />
      <Handle type="source" position={Position.Right} className="!bg-[var(--accent)]" />

      {/* Header row: runtime icon + name + status pill */}
      <div className="flex items-center justify-between gap-2">
        <span className="flex min-w-0 items-center gap-2 truncate text-sm font-medium">
          <RuntimeIcon runtime={data.service.spec.runtime} />
          <span className="truncate">{shortName}</span>
        </span>
        <DeployStatusPill status={status} />
      </div>

      {/* URL pill */}
      <div className="mt-2.5">
        {url ? (
          <UrlPill url={url} />
        ) : (
          <span className="inline-block font-mono text-[10px] text-[var(--text-tertiary)]">
            no URL yet
          </span>
        )}
      </div>

      {/* Footer: replicas (live/desired) + sleep badge if applicable */}
      <div className="mt-2.5 flex items-center justify-between gap-2 border-t border-[var(--border-subtle)] pt-2 font-mono text-[10px]">
        <ReplicasBadge replicas={replicas} status={status} />
        {status === "sleeping" && <SleepBadge />}
      </div>
    </div>
  );
}

function ReplicasBadge({
  replicas,
  status,
}: {
  replicas: Replicas | null;
  status: DeployStatus;
}) {
  if (!replicas) {
    return (
      <span className="text-[var(--text-tertiary)]">
        replicas <span className="text-[var(--text-secondary)]">—</span>
      </span>
    );
  }
  const allUp = replicas.max > 0 && replicas.ready === replicas.max;
  const someUp = replicas.ready > 0;
  const dotCls = allUp
    ? "bg-emerald-400"
    : !someUp
      ? "bg-[var(--text-tertiary)]/40"
      : "bg-amber-400";
  return (
    <span className="inline-flex items-center gap-2 text-[var(--text-tertiary)]">
      <span className="inline-flex items-center gap-1.5">
        <span className={cn("h-1.5 w-1.5 shrink-0 rounded-full", dotCls)} />
        <span>
          <span className="text-[var(--text-secondary)]">{replicas.ready}</span>
          <span className="text-[var(--text-tertiary)]/60">/{replicas.max}</span>{" "}
          <span className="text-[var(--text-tertiary)]/80">
            {status === "sleeping" ? "asleep" : "ready"}
          </span>
        </span>
      </span>
      {replicas.cpuPct !== undefined && replicas.ready > 0 && (
        <span
          title="Average CPU vs container limit"
          className="inline-flex items-center gap-1 rounded bg-[var(--bg-tertiary)]/60 px-1.5 py-0.5 font-mono"
        >
          <span className="text-[var(--text-secondary)]">{replicas.cpuPct}</span>
          <span className="text-[var(--text-tertiary)]/70">%</span>
        </span>
      )}
    </span>
  );
}

function UrlPill({ url }: { url: string }) {
  const [copied, setCopied] = useState(false);
  const display = url.replace(/^https?:\/\//, "");

  const onCopy = async (e: React.MouseEvent) => {
    e.preventDefault();
    e.stopPropagation();
    try {
      await navigator.clipboard.writeText(url);
      setCopied(true);
      toast.success("URL copied");
      window.setTimeout(() => setCopied(false), 1200);
    } catch {
      toast.error("Couldn't copy");
    }
  };

  return (
    <span
      // Stop the click from bubbling up to React Flow's onNodeClick
      // (which would open the overlay). The user explicitly clicked
      // the URL pill — they want the URL action, not the panel.
      onClick={(e) => e.stopPropagation()}
      className="inline-flex max-w-full items-center gap-1 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-tertiary)] pl-2 pr-1 py-0.5 font-mono text-[10px] text-[var(--text-secondary)]"
    >
      <a
        href={url}
        target="_blank"
        rel="noreferrer"
        className="inline-flex min-w-0 items-center gap-1 truncate hover:text-[var(--accent)]"
      >
        <span className="truncate">{display}</span>
        <ExternalLink className="h-2.5 w-2.5 shrink-0" />
      </a>
      <button
        type="button"
        onClick={onCopy}
        aria-label="Copy URL"
        className="inline-flex h-4 w-4 shrink-0 items-center justify-center rounded text-[var(--text-tertiary)] hover:bg-[var(--bg-primary)] hover:text-[var(--text-primary)]"
      >
        {copied ? <Check className="h-2.5 w-2.5 text-emerald-400" /> : <Copy className="h-2.5 w-2.5" />}
      </button>
    </span>
  );
}
