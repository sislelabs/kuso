"use client";

import { useLiveServiceUrl } from "@/features/projects/liveDomain";
import { lastDeployedAt } from "@/features/projects/facts";
import { useState } from "react";
import { Check, Copy, ExternalLink, Layers, MoreHorizontal } from "lucide-react";
import type { KusoEnvironment, KusoService } from "@/types/projects";
import {
  deriveServiceStatus,
  serviceUrl,
  STATUS_TONE,
  type BuildSummary,
  type NodeStatus,
} from "@/features/services";
import { buildRefLabel } from "@/features/builds";
import { RuntimeIcon } from "@/components/service/RuntimeIcon";
import { cn, serviceShortName } from "@/lib/utils";
import { relativeTime } from "@/lib/format";
import { stuckFacts } from "@/lib/status";
import { toast } from "sonner";
import { ErrorBadge, VulnBadge } from "./NodeBadges";
import type { ErrorSummary, VulnSummary } from "./badgeSummary";
import { NodeHandles } from "./NodeHandles";
import { nodeFocusClass, type CanvasNodeA11y } from "./nodeA11y";
import { serviceNodeWidth } from "./geometry";
import { nodeActionButtonClass, nodeBorderClass, nodeCardClass, nodeNameClass, nodeRailClass } from "./nodeStyle";
import { StagedDot } from "@/components/staged";

export interface ServiceNodeData extends CanvasNodeA11y {
  project: string;
  service: KusoService;
  env?: KusoEnvironment;
  // Latest build for this service in the selected env group. Polled at
  // the canvas level, faster than the describe payload refreshes.
  latestBuild?: BuildSummary;
  vulns?: VulnSummary;
  errors?: ErrorSummary;
  // Injected by ProjectCanvas — fires the right-click context menu with
  // the URL this card shows, so "Open URL" goes to the same place.
  __onContext?: (e: React.MouseEvent, liveUrl?: string) => void;
  // Injected by ProjectCanvas — opens the service overlay on a tab.
  __onOpenTab?: (tab: string) => void;
}

interface Replicas {
  ready: number;
  desired: number;
  max: number;
}

function replicasFor(env?: KusoEnvironment): Replicas | null {
  const r = env?.status?.replicas as { ready?: number; desired?: number; max?: number } | undefined;
  if (!r || (r.desired === undefined && r.ready === undefined && r.max === undefined)) return null;
  const ceil = r.max ?? r.desired ?? 0;
  return { ready: r.ready ?? 0, desired: r.desired ?? ceil, max: ceil };
}

export function serviceNodeStatus(data: Pick<ServiceNodeData, "service" | "env" | "latestBuild">): NodeStatus {
  return deriveServiceStatus({ stopped: !!data.service.spec?.stopped, env: data.env, latestBuild: data.latestBuild });
}

export function ServiceNode({ id, data }: { id: string; data: ServiceNodeData }) {
  const status = serviceNodeStatus(data);
  const tone = STATUS_TONE[status.tone];
  const internal = !!data.service.spec?.internal;
  const replicas = replicasFor(data.env);
  const shortName = serviceShortName(data.project, data.service.metadata.name);
  const url = useLiveServiceUrl(data.project, shortName, data.env, serviceUrl(data.service, data.env));
  const displayName = data.service.spec.displayName?.trim() || shortName;
  const deployedAt = lastDeployedAt(data.env);
  const dimmed = status.kind === "sleeping" || status.kind === "stopped";
  const live = liveBuildOf(data.latestBuild);
  const hasBadges = (data.errors?.occurrences ?? 0) > 0 || (data.vulns?.fixableCritical ?? 0) > 0;

  return (
    <div
      data-node-context
      data-canvas-node={id}
      data-focus-key={`service:${shortName}`}
      tabIndex={data.__tabIndex ?? -1}
      role="group"
      aria-roledescription="service"
      aria-label={[displayName, status.label, data.errors?.occurrences ? `${data.errors.occurrences} errors in 24h` : ""]
        .filter(Boolean)
        .join(", ")}
      onContextMenu={(e) => data.__onContext?.(e, url)}
      style={{ width: serviceNodeWidth(displayName) }}
      title={[status.detail, url ? buildRefText(data.latestBuild)?.title : ""].filter(Boolean).join("\n") || undefined}
      className={cn(
        nodeCardClass,
        "flex min-h-[120px] flex-col p-3 pt-3.5",
        nodeBorderClass(status.tone, tone.border),
        dimmed && "opacity-70",
        nodeFocusClass,
      )}
    >
      <NodeHandles />
      <span aria-hidden className={cn(nodeRailClass, tone.dot)} />

      <button
        tabIndex={-1}
        type="button"
        aria-label={`Actions for ${displayName}`}
        onClick={(e) => {
          e.preventDefault();
          e.stopPropagation();
          data.__onContext?.(e, url);
        }}
        className={nodeActionButtonClass}
      >
        <MoreHorizontal className="h-3.5 w-3.5" />
      </button>

      {/* The name owns its row (the card widens for it); badges sit on
          their own row so they never squeeze it. */}
      <div className="flex min-w-0 items-center gap-2 pr-6">
        <RuntimeIcon runtime={data.service.spec.runtime} />
        <span className={nodeNameClass} title={displayName}>
          {displayName}
        </span>
        <StagedDot project={data.project} service={shortName} className="shrink-0" />
      </div>
      {hasBadges && (
        <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
          <ErrorBadge compact tabIndex={-1} summary={data.errors} onOpen={() => data.__onOpenTab?.("errors")} />
          <VulnBadge tabIndex={-1} summary={data.vulns} onOpen={() => data.__onOpenTab?.("vulnerabilities")} />
        </div>
      )}

      {/* The URL gets the whole row; the commit moves to the tooltip
          then, and only takes this row when there's no URL to show. */}
      <div className="mt-2 flex min-w-0 items-center gap-2">
        {url ? (
          <UrlPill url={url} />
        ) : (
          <>
            <span className="shrink-0 text-xs text-[var(--text-tertiary)]">{data.service.spec.runtime === "worker" ? "Background worker" : internal ? "Internal only" : "No public URL"}</span>
            <BuildRef build={data.latestBuild} />
          </>
        )}
      </div>

      <div className="flex-1" />

      <div className="flex items-center justify-between gap-2 border-t border-[var(--border-subtle)] pt-2 text-xs">
        <span className="flex min-w-0 items-center gap-1.5">
          <span aria-hidden className={cn("h-2 w-2 shrink-0 rounded-full", tone.dot)} />
          <span className={cn("shrink-0 font-medium", tone.text)}>{status.label}</span>
          {live?.rolledBack && (
            <span
              className="shrink-0 rounded-full border border-[var(--border-default)] bg-[var(--bg-tertiary)] px-1.5 font-medium text-[var(--text-secondary)]"
              title="Rolled back to an older build. The next deploy replaces it."
            >
              Rolled back
            </span>
          )}
          {/* A stuck rollout isn't "deployed 1m ago"; its reason takes the slot. */}
          {status.kind === "stuck" ? (
            <span className="truncate text-[var(--text-secondary)]" title={status.detail}>
              · {status.detail || stuckFacts(data.env?.status)?.summary || "Can't start"}
            </span>
          ) : deployedAt && (
            <span className="truncate text-[var(--text-tertiary)]" title={`Deployed ${new Date(deployedAt).toLocaleString()}`}>
              · {live?.rolledBack ? "" : "deployed "}{relativeTime(deployedAt)}
            </span>
          )}
        </span>
        {replicas && !dimmed && (
          <span
            className="inline-flex shrink-0 items-center gap-1 tabular-nums text-[var(--text-secondary)]"
            title={`${replicas.ready} of ${replicas.desired} replicas ready${replicas.max > replicas.desired ? `, autoscales up to ${replicas.max}` : ""}`}
          >
            <Layers className="h-3 w-3 text-[var(--text-tertiary)]" aria-hidden />
            {replicas.ready}/{replicas.desired}
            <span className="sr-only"> replicas ready</span>
          </span>
        )}
      </div>
    </div>
  );
}

// LiveBuildRef is the build the env actually runs when it isn't the
// latest one (after a rollback, or when the newest build failed). The
// server adds it to the latest-builds response; older servers omit it.
interface LiveBuildRef {
  id: string;
  branch?: string;
  commitSha?: string;
  ref?: string;
  rolledBack?: boolean;
}

function liveBuildOf(build?: BuildSummary): LiveBuildRef | undefined {
  const v: unknown = (build as { live?: unknown } | undefined)?.live;
  if (!v || typeof v !== "object") return undefined;
  const o = v as Record<string, unknown>;
  if (typeof o.id !== "string") return undefined;
  const str = (k: string) => (typeof o[k] === "string" ? (o[k] as string) : undefined);
  return { id: o.id, branch: str("branch"), commitSha: str("commitSha"), ref: str("ref"), rolledBack: o.rolledBack === true };
}

// The commit that's serving: the live build when the server reports one,
// else the latest build. Shown inline only on cards without a URL (a
// failed latest build turns it red); otherwise it's in the card tooltip.
function buildRefText(build?: BuildSummary) {
  if (!build) return undefined;
  const live = liveBuildOf(build);
  const latestSha = buildRefLabel(build, 7);
  const st = (build.status ?? "").toLowerCase();
  const failed = st === "failed" || st === "error" || st === "release-failed";
  const sha = live ? buildRefLabel(live, 7) : latestSha;
  const label = `${(live ? live.branch : build.branch) || build.branch || "main"}${sha ? `@${sha}` : ""}`;
  const latestLabel = `${build.branch || "main"}${latestSha ? `@${latestSha}` : ""}`;
  const title = live
    ? `Running ${label}. Latest build: ${latestLabel} (${st || "unknown"})`
    : `Latest build: ${label} (${st || "unknown"})`;
  return { label, title, failed, live };
}

function BuildRef({ build }: { build?: BuildSummary }) {
  const t = buildRefText(build);
  if (!t) return null;
  return (
    <span
      className={cn(
        "ml-auto min-w-[3.5rem] shrink-[4] truncate font-mono text-xs",
        t.failed && !t.live ? "text-[var(--error)]" : "text-[var(--text-tertiary)]",
      )}
      title={t.title}
    >
      {t.failed && !t.live && <span className="sr-only">Latest build failed: </span>}
      {t.live && <span className="sr-only">Running </span>}
      {t.label}
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

  // Only the two small icons are their own targets; the host text is
  // part of the card, so clicking it opens the service like the rest.
  return (
    <span className="inline-flex min-w-0 max-w-full items-center gap-0.5 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-tertiary)] py-0.5 pl-2 pr-0.5 font-mono text-xs text-[var(--text-secondary)]">
      <span className="truncate pr-1" title={url}>{display}</span>
      <a
        tabIndex={-1}
        href={url}
        target="_blank"
        rel="noreferrer"
        onClick={(e) => e.stopPropagation()}
        aria-label={`Open ${display} in a new tab`}
        className="nodrag inline-flex h-5 w-5 shrink-0 items-center justify-center rounded text-[var(--text-tertiary)] hover:bg-[var(--bg-primary)] hover:text-[var(--accent)]"
      >
        <ExternalLink className="h-3 w-3" aria-hidden />
      </a>
      <button
        tabIndex={-1}
        type="button"
        onClick={onCopy}
        aria-label={`Copy ${display}`}
        className="nodrag inline-flex h-5 w-5 shrink-0 items-center justify-center rounded text-[var(--text-tertiary)] hover:bg-[var(--bg-primary)] hover:text-[var(--text-primary)]"
      >
        {copied ? <Check className="h-3 w-3 text-[var(--success)]" /> : <Copy className="h-3 w-3" />}
      </button>
    </span>
  );
}
