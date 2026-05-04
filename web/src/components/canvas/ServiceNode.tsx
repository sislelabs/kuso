"use client";

import { useState } from "react";
import { Handle, Position } from "@xyflow/react";
import { Check, Copy, ExternalLink } from "lucide-react";
import type { KusoEnvironment, KusoService } from "@/types/projects";
import { type DeployStatus } from "@/components/service/DeployStatusPill";
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
  // Heuristic for "stuck failed": env has desired replicas but none
  // are ready AND status hasn't reported any other phase. Catches the
  // common case where a build failure leaves env.status.phase empty
  // (no deploy ever happened) — without this, the canvas falls back
  // to "unknown" and the failed service paints as a generic
  // hover-orange border instead of red.
  const r = env.status?.replicas as { ready?: number; max?: number; desired?: number } | undefined;
  const desired = r?.max ?? r?.desired ?? 0;
  const ready = r?.ready ?? 0;
  if (desired > 0 && ready === 0) return "failed";
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
        // Fixed height (5 × 24px grid units) so service nodes line up
        // horizontally with addon nodes — see AddonNode for the
        // matching value. Content (header/url/replicas) is given
        // breathing space inside via the existing margins.
        // border-2 (vs border-1) so status hue (green/amber/red) is
        // unambiguously visible at canvas zoom levels — same fix as
        // AddonNode.
        "group flex h-[120px] w-[280px] flex-col rounded-2xl border-2 bg-[var(--bg-elevated)] p-3 transition-colors cursor-pointer",
        "hover:border-[var(--border-strong)]",
        (status === "building" || status === "deploying") &&
          "border-[var(--building)]/70 animate-pulse",
        status === "active" && "border-emerald-500/60",
        status === "failed" && "border-red-500/60",
        status === "sleeping" && "opacity-60 border-[var(--border-strong)]",
        !["building", "deploying", "active", "failed", "sleeping"].includes(status) &&
          "border-[var(--border-strong)]"
      )}
    >
      <Handle type="target" position={Position.Left} className="!bg-[var(--accent)]" />
      <Handle type="source" position={Position.Right} className="!bg-[var(--accent)]" />

      {/* Header row: runtime icon + name + uptime age. The
          DeployStatusPill ("ACTIVE") was removed — running state is
          already encoded by the green border + the replica dot, so
          the pill was redundant noise. Uptime tells you something
          new: how long this revision has been live. */}
      <div className="flex items-center justify-between gap-2">
        <span className="flex min-w-0 items-center gap-2 truncate text-sm font-medium">
          <RuntimeIcon runtime={data.service.spec.runtime} />
          <span className="truncate">{shortName}</span>
        </span>
        <UptimeBadge env={data.env} status={status} />
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
  // Replicas are a load proxy: more pods running = more traffic.
  // Color the dot by ready/max ratio so a glance tells you "comfy
  // / busy / scaling cliff":
  //   ratio == 0      grey   — nothing up (failed/sleeping/etc)
  //   ratio  < 0.5    green  — comfortably under capacity
  //   ratio  < 0.85   orange — mid-load, autoscaler probably climbing
  //   ratio >= 0.85   red    — near max, may need more headroom
  // Sleeping overrides everything (greys out + label changes to asleep).
  const ratio = replicas.max > 0 ? replicas.ready / replicas.max : 0;
  let dotCls = "bg-[var(--text-tertiary)]/40";
  if (status !== "sleeping" && replicas.ready > 0) {
    if (ratio >= 0.85) dotCls = "bg-red-400";
    else if (ratio >= 0.5) dotCls = "bg-amber-400";
    else dotCls = "bg-emerald-400";
  }
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

// UptimeBadge shows how long this revision has been live ("3h",
// "2d"). Only renders when the env is in a steady state (not
// building/deploying — those have their own animated border, no
// uptime to report). Reads env.status.lastDeployedAt so the value
// resets to 0 on every redeploy, not on pod restart.
function UptimeBadge({
  env,
  status,
}: {
  env?: KusoEnvironment;
  status: DeployStatus;
}) {
  if (!env || status === "building" || status === "deploying") return null;
  const ts = env.status?.lastDeployedAt;
  if (!ts) return null;
  return (
    <span
      title={`Last deployed at ${ts}`}
      className="shrink-0 font-mono text-[10px] text-[var(--text-tertiary)]"
    >
      {relativeAge(ts)}
    </span>
  );
}

// relativeAge → "32s", "5m", "3h", "2d". Same renderer the build
// list uses; inlined here so this file doesn't reach into a sibling.
function relativeAge(iso: string): string {
  const t = new Date(iso).getTime();
  if (!Number.isFinite(t)) return "?";
  const sec = Math.max(0, Math.floor((Date.now() - t) / 1000));
  if (sec < 60) return `${sec}s`;
  const min = Math.floor(sec / 60);
  if (min < 60) return `${min}m`;
  const hr = Math.floor(min / 60);
  if (hr < 24) return `${hr}h`;
  return `${Math.floor(hr / 24)}d`;
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
