"use client";

import { useState } from "react";
import { Handle, Position } from "@xyflow/react";
import { Check, Copy, ExternalLink } from "lucide-react";
import type { KusoEnvironment, KusoService } from "@/types/projects";
import type { BuildSummary } from "@/features/services/api";
import { type DeployStatus } from "@/components/service/DeployStatusPill";
import { SleepBadge } from "@/components/service/SleepBadge";
import { RuntimeIcon } from "@/components/service/RuntimeIcon";
import { cn, serviceShortName } from "@/lib/utils";
import { toast } from "sonner";

export interface ServiceNodeData extends Record<string, unknown> {
  project: string;
  service: KusoService;
  env?: KusoEnvironment;
  // Latest build for this service. Polled at the canvas level so
  // statusFor can prefer fresh build state over a stale env phase.
  latestBuild?: BuildSummary;
  // Injected by ProjectCanvas — fires the right-click context menu.
  __onContext?: (e: React.MouseEvent) => void;
}

function statusFor(env?: KusoEnvironment, latestBuild?: BuildSummary): DeployStatus {
  // Source-of-truth ordering, in priority:
  //
  // 1. Latest build is in-flight → building/deploying. Build state
  //    is the freshest signal — the moment a user clicks Redeploy,
  //    the build CR transitions to "pending" and the env CR is far
  //    behind. Without checking builds first, the canvas would
  //    keep showing the old failed/active state until the build
  //    actually rolled.
  //
  // 2. env.ready = true → active. Steady-state win condition.
  //
  // 3. Latest build failed AND env not ready → failed. Distinct
  //    from "0 replicas" because a brand-new service with a failed
  //    first build has 0 desired replicas yet, so the replica
  //    heuristic alone misses it.
  //
  // 4. env has desired replicas but 0 ready → failed (catches the
  //    "stale env.phase=building" case where the operator never
  //    wrote phase=failed back).
  //
  // 5. Fall through to env.status.phase as the last resort.
  const buildStatus = (latestBuild?.status ?? "").toLowerCase();
  if (buildStatus === "pending" || buildStatus === "running" || buildStatus === "building") {
    return "building";
  }
  if (buildStatus === "deploying") return "deploying";

  if (!env) return "unknown";
  if (env.status?.ready) return "active";

  if (buildStatus === "failed" || buildStatus === "error") return "failed";

  const r = env.status?.replicas as { ready?: number; max?: number; desired?: number } | undefined;
  const desired = r?.max ?? r?.desired ?? 0;
  const ready = r?.ready ?? 0;
  if (desired > 0 && ready === 0) return "failed";

  const phase = (env.status?.phase ?? "").toString().toLowerCase();
  if (phase === "building") return "building";
  if (phase === "deploying") return "deploying";
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
  const status = statusFor(data.env, data.latestBuild);
  // Prefer a custom domain when one's set on the service spec, falling
  // back to the env's auto-domain. The custom domain is the user's
  // deliberate choice (Settings → Networking → Domains); the auto one
  // is the kuso.sislelabs.com fallback. If a service is internal-only
  // (no Ingress at all), env.status.url is empty too — the pill below
  // renders "internal only" in that case.
  const customDomain = data.service.spec.domains?.find((d) => d?.host)?.host;
  const customTLS =
    data.service.spec.domains?.find((d) => d?.host)?.tls ?? true;
  const url = customDomain
    ? `${customTLS ? "https" : "http"}://${customDomain}`
    : (data.env?.status?.url as string | undefined);
  const replicas = replicasFor(data.env);
  // Display the user-supplied label when set; fall back to the slug
  // for back-compat with services created before v0.7.43 (no
  // displayName) or services where the user blanked it.
  const shortName = serviceShortName(data.project, data.service.metadata.name);
  const displayName = data.service.spec.displayName?.trim() || shortName;

  return (
    <div
      data-node-context
      onContextMenu={data.__onContext}
      className={cn(
        // Fixed height (5 × 24px grid units = 120px). Footer packs
        // replicas + build line on one row, so we don't need the
        // extra cell that the previous standalone build line ate.
        // border-2 (vs border-1) so status hue (green/amber/red) is
        // unambiguously visible at canvas zoom levels.
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

      {/* Header row: runtime icon + name + uptime age. Uptime sits
          on the same row as the name for fast scanning ("how long
          has this revision been live?" is asked at the same time as
          "what is this service?"). One size up so it's legible at
          canvas zoom. The build line + replicas split the footer
          row instead. */}
      <div className="flex items-center justify-between gap-2">
        <span className="flex min-w-0 items-center gap-2 truncate text-sm font-medium">
          <RuntimeIcon runtime={data.service.spec.runtime} />
          <span className="truncate">{displayName}</span>
        </span>
        <UptimeBadge env={data.env} status={status} />
      </div>

      {/* URL pill */}
      <div className="mt-2">
        {url ? (
          <UrlPill url={url} />
        ) : (
          <span className="inline-block font-mono text-[10px] text-[var(--text-tertiary)]">
            no URL yet
          </span>
        )}
      </div>

      {/* Spacer pushes the footer to the bottom of the fixed-height
          card so the URL row stays glued to the header above. */}
      <div className="flex-1" />

      {/* Footer row: replicas left, build line right (or sleep badge
          if applicable). One row, scannable corners — left tells you
          health, right tells you what code is running. */}
      <div className="flex items-center justify-between gap-2 border-t border-[var(--border-subtle)] pt-2 font-mono text-[10px]">
        <ReplicasBadge replicas={replicas} status={status} />
        {status === "sleeping" ? <SleepBadge /> : <BuildLine build={data.latestBuild} />}
      </div>
    </div>
  );
}

// BuildLine renders a one-row build summary on the canvas card —
// "main@a4b2f1c · ✓". Lives in the footer alongside ReplicasBadge,
// so left = health, right = what code is running. Click on the
// node still opens the overlay where the full Deployments tab
// lives; this is just glance info.
function BuildLine({ build }: { build?: BuildSummary }) {
  if (!build) return null;
  const sha = (build.commitSha ?? "").slice(0, 7);
  const branch = build.branch || "main";
  const status = (build.status ?? "").toLowerCase();
  // Status glyph + color via the shared token vocabulary so it
  // matches the canvas border state for the same condition.
  let glyph = "·";
  let cls = "text-[var(--text-tertiary)]";
  if (status === "succeeded") {
    glyph = "✓";
    cls = "text-emerald-400";
  } else if (status === "failed" || status === "error") {
    glyph = "✗";
    cls = "text-red-400";
  } else if (status === "cancelled" || status === "superseded") {
    // Distinct from failed (red) — the build didn't break, it was
    // replaced by a newer one. Muted gray so a feed of redeploys
    // doesn't read as a wall of red.
    glyph = "⊘";
    cls = "text-[var(--text-tertiary)]";
  } else if (status === "running" || status === "pending" || status === "building") {
    glyph = "…";
    cls = "text-[var(--building)]";
  }
  return (
    <span className="truncate text-[var(--text-tertiary)]">
      <span className="text-[var(--text-secondary)]">{branch}</span>
      {sha && (
        <>
          <span className="text-[var(--text-tertiary)]/60">@</span>
          <span className="text-[var(--text-secondary)]">{sha}</span>
        </>
      )}
      {" "}
      <span className={cn("ml-0.5", cls)}>{glyph}</span>
    </span>
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
      className="shrink-0 font-mono text-xs text-[var(--text-secondary)]"
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
