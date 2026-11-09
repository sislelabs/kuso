"use client";

import { ApiError } from "@/lib/api-client";
import { formatLatency, relativeTime } from "@/lib/format";
import { TONE_CLASSES, type StatusMeta, type StatusTone } from "@/lib/status";
import { cn } from "@/lib/utils";
import { isCurrentPod, useServicePods } from "@/features/pods";
import { lastDeployedAt } from "@/features/projects/facts";
import { useProjectUptime, uptimeView, type UptimeServiceStatus } from "@/features/uptime";
import type { KusoEnvironment } from "@/types/projects";
import { Rocket, Wrench } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useCanOnProject, Perms } from "@/features/auth";
import type { FirstRun } from "./firstRun";
import type { FixFirst } from "../workspace/fixFirst";
import { buildLogAnchor, neverStartedCause } from "./firstRunContext";
import { useRedeploy } from "./ServiceHeaderActions";
import { unretryableReason } from "./BuildRow";

export interface ServiceHealthStripProps {
  project: string;
  service: string;
  env: KusoEnvironment | undefined;
  // Env group short name ("production", "staging", "preview-pr-7").
  envGroup: string;
  // The same status the header pill shows, so the two never disagree.
  status: StatusMeta;
  // The cause behind a failing / degraded status (the selector's detail).
  statusDetail?: string;
  // The problem banner (cause + fixes), folded in as the strip's first
  // row so a failing service shows one block instead of two.
  alert?: React.ReactNode;
  // Leads the meta line (the service URL).
  lead?: React.ReactNode;
  // Never-run services collapse the strip into one banner.
  firstRun?: FirstRun;
  // A known failure's fix (Change branch, …): the banner's primary action.
  fix?: FixFirst;
  // The Deployments tab is open: its failed build row shows the reason.
  onDeployments?: boolean;
  onNavigate: (tab: string, anchor?: string) => void;
}

interface Fact {
  id: string;
  text: string;
  tone: StatusTone | "plain";
  title: string;
  tab: string;
  anchor?: string;
}

// ServiceHealthStrip is the one-line meta under the overlay title: ready
// count, uptime ping, restarts and last deploy, each a link to the tab it
// came from. A problem banner, when there is one, sits under it.
export function ServiceHealthStrip(props: ServiceHealthStripProps) {
  if (props.firstRun?.kind === "never") {
    return (
      <>
        {props.lead ? <div className="mt-1 flex min-w-0 items-center text-xs text-[var(--text-secondary)]">{props.lead}</div> : null}
        <NeverRanBanner {...props} firstRun={props.firstRun} />
      </>
    );
  }
  return <HealthCells {...props} />;
}

function NeverRanBanner({
  project,
  service,
  env,
  firstRun,
  fix,
  onDeployments,
  onNavigate,
}: ServiceHealthStripProps & { firstRun: Extract<FirstRun, { kind: "never" }> }) {
  const redeploy = useRedeploy(project, service, env);
  const canWrite = useCanOnProject(project, Perms.ServicesWrite);
  const anchor = buildLogAnchor(firstRun);
  const lb = firstRun.lastBuild;
  // The extracted fatal line beats the classifier's generic summary.
  const reason = firstRun.failed ? lb?.failureClass?.lineHint || lb?.errorMessage || lb?.failureClass?.summary : undefined;
  // A build that can't succeed as-is (missing branch) isn't retried.
  const blocked = firstRun.failed && lb ? unretryableReason(lb) : undefined;
  const tone = firstRun.failed ? "danger" : firstRun.building ? "progress" : "info";
  return (
    <div
      role={firstRun.failed ? "alert" : "status"}
      className={cn(
        "mt-3 flex flex-wrap items-center gap-x-3 gap-y-2 rounded-lg border px-3 py-2.5",
        tone === "danger"
          ? "border-[var(--error)]/35 bg-[var(--error)]/5"
          : "border-[var(--border-subtle)] bg-[var(--bg-secondary)]",
      )}
    >
      <div className="min-w-0 flex-1">
        <p className="text-sm font-medium text-[var(--text-primary)]">
          Never deployed here · <span className={firstRun.failed ? "text-[var(--error)]" : undefined}>{neverStartedCause(firstRun).replace(/\.$/, "")}</span>
        </p>
        {reason && !onDeployments && (
          <p className="mt-0.5 line-clamp-2 break-words text-xs text-[var(--text-secondary)]" title={reason}>
            {reason}
          </p>
        )}
      </div>
      <div className="flex shrink-0 items-center gap-1.5">
        {firstRun.lastBuild && !(onDeployments && firstRun.failed) && (
          <Button size="sm" variant="secondary" className="h-7 px-2.5 text-xs" onClick={() => onNavigate("deployments", anchor)}>
            {firstRun.building ? "Follow the build" : "View build log"}
          </Button>
        )}
        {fix && fix.id !== "image" && firstRun.failed && (
          <Button size="sm" className="h-7 px-2.5 text-xs" onClick={() => onNavigate(fix.tab, fix.anchor)} disabled={!canWrite}>
            <Wrench className="h-3 w-3" aria-hidden />
            {fix.label}
          </Button>
        )}
        {!redeploy.isImage && !firstRun.building && !(fix && firstRun.failed) && (
          <Button
            size="sm"
            className="h-7 px-2.5 text-xs"
            onClick={redeploy.request}
            disabled={!canWrite || redeploy.pending || !!blocked}
            disabledReason={!canWrite ? "Requires editor access on this project" : blocked}
          >
            <Rocket className="h-3 w-3" aria-hidden />
            {redeploy.pending ? "Starting…" : firstRun.failed && !blocked ? "Retry deploy" : "Deploy"}
          </Button>
        )}
      </div>
      {redeploy.dialog}
    </div>
  );
}

function HealthCells({ project, service, env, envGroup, status, statusDetail, alert, lead, onNavigate }: ServiceHealthStripProps) {
  const pods = useServicePods(project, service, env?.metadata?.name);
  const uptime = useProjectUptime(project);

  // Finished pods, a draining old ReplicaSet and release-hook pods aren't instances.
  const podList =
    pods.data?.pods?.filter((p) => isCurrentPod(p) && p.phase !== "Succeeded" && p.phase !== "Failed") ?? null;
  // Ready/desired from the env's rollup, the numbers the canvas and list
  // show; a pod list mid-rollout still holds the old ReplicaSet's pods.
  const replicas = env?.status?.replicas as { ready?: number; desired?: number } | undefined;
  const hasRollup = typeof replicas?.desired === "number";
  const ready = hasRollup ? (replicas?.ready ?? 0) : (podList?.filter((p) => p.ready).length ?? 0);
  const total = hasRollup ? (replicas?.desired ?? 0) : (podList?.length ?? 0);
  // While the status is a problem, "1/1 ready" or "uptime ok" next to it
  // reads as a contradiction; the cause takes their place.
  const problem = status.tone === "danger" || status.tone === "warning";
  const httpService = !env || !!env.spec.host || (env.spec.additionalHosts?.length ?? 0) > 0;
  const restarts = podList?.reduce((n, p) => n + (p.restarts ?? 0), 0) ?? 0;
  const crashing = podList?.some((p) => /crashloop/i.test(p.reason ?? "")) ?? false;
  const deployedAt = lastDeployedAt(env);

  // The status itself is the header pill and the error count is the Errors
  // tab badge, so the line only carries what neither shows.
  const facts: Fact[] = [];
  if (problem && !alert) {
    facts.push({
      id: "reason",
      text: statusDetail || status.label,
      tone: status.tone,
      title: statusDetail || status.label,
      tab: "deployments",
    });
  }
  if (!problem && total > 0) {
    facts.push({
      id: "ready",
      text: `${ready}/${total} ready`,
      tone: ready === total ? "plain" : ready === 0 ? "danger" : "warning",
      title: `${ready} of ${total} instances ready`,
      tab: "deployments",
    });
  }
  if (!problem) {
    const up = uptimeFact(uptime.data?.enabled, pickUptime(uptime.data?.services ?? null, service, env, envGroup), uptime.error, httpService);
    if (up) facts.push(up);
  }
  if (restarts > 0) {
    facts.push({
      id: "restarts",
      text: `${restarts.toLocaleString()} ${restarts === 1 ? "restart" : "restarts"}`,
      tone: crashing ? "danger" : "warning",
      title: "Container restarts across the running instances, counted since each instance started. Open logs to see why.",
      tab: "logs",
    });
  }
  if (deployedAt) {
    facts.push({
      id: "deploy",
      text: `deployed ${relativeTime(deployedAt)}`,
      tone: "plain",
      title: `Current release went live ${new Date(deployedAt).toLocaleString()}`,
      tab: "deployments",
    });
  }

  const line = (
    <ul aria-label="Service health" className="mt-1 flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-0.5 text-xs text-[var(--text-secondary)]">
      {lead ? <li className="flex min-w-0 max-w-full items-center">{lead}</li> : null}
      {facts.map((f, i) => (
        <li key={f.id} className="flex min-w-0 items-center gap-1.5">
          {(i > 0 || lead) && (
            <span aria-hidden className="text-[var(--text-tertiary)]">
              ·
            </span>
          )}
          <button
            type="button"
            title={f.title}
            aria-label={`${f.text}. ${f.title}`}
            onClick={() => onNavigate(f.tab, f.anchor)}
            className={cn(
              "min-w-0 truncate whitespace-nowrap rounded-sm hover:text-[var(--text-primary)] hover:underline max-sm:py-1.5",
              f.tone === "plain" ? undefined : cn("font-medium", TONE_CLASSES[f.tone].text),
            )}
          >
            {f.text}
          </button>
        </li>
      ))}
    </ul>
  );
  if (!alert) return line;
  return (
    <>
      {line}
      <div
        className={cn(
          "mt-2.5 overflow-hidden rounded-lg border",
          status.tone === "danger" ? "border-[var(--danger)]/35" : "border-[var(--warning)]/35",
        )}
      >
        {alert}
      </div>
    </>
  );
}

// The checker reports one row per checked env, keyed by env CR name; it
// only checks production today, which is also what older servers meant
// when they sent no env.
function pickUptime(list: UptimeServiceStatus[] | null, service: string, env: KusoEnvironment | undefined, envGroup: string): UptimeServiceStatus | undefined {
  const mine = (list ?? []).filter((s) => s.service === service);
  const byName = env ? mine.find((s) => s.env === env.metadata.name) : undefined;
  return byName ?? (envGroup === "production" ? mine.find((s) => !s.env) : undefined);
}

// uptimeFact is the uptime check in a few words, or nothing when there's
// no check to report on (no endpoint, checker off, not set up, paused
// alongside a sleeping or stopped status the pill already shows).
function uptimeFact(
  enabled: boolean | undefined,
  s: UptimeServiceStatus | undefined,
  error: unknown,
  httpService: boolean,
): Fact | null {
  if (!httpService || enabled === false) return null;
  if (error instanceof ApiError && error.status === 404) return null;
  const v = uptimeView(s);
  const base = { id: "uptime", tab: "settings", anchor: "uptime" } as const;
  const checked = s?.lastCheckedAt ? `checked ${relativeTime(s.lastCheckedAt)}` : "not checked yet";
  const stale = v.stale ? "last known" : "";
  switch (v.state) {
    case "up":
      return {
        ...base,
        text: v.latencyMs ? `up ${formatLatency(v.latencyMs)}` : "uptime ok",
        tone: "plain",
        title: [`${s?.url ?? ""} answered${s?.statusCode ? ` HTTP ${s.statusCode}` : ""}`, checked, stale].filter(Boolean).join(" · "),
      };
    case "failing":
      return { ...base, text: "uptime failing", tone: "warning", title: [s?.reason || s?.error, checked, stale].filter(Boolean).join(" · ") };
    case "down":
      return {
        ...base,
        text: s?.since ? `down for ${relativeTime(s.since).replace(/ ago$/, "")}` : "uptime down",
        tone: "danger",
        title: [s?.reason || s?.error, s?.since ? `down since ${new Date(s.since).toLocaleString()}` : "", checked, stale]
          .filter(Boolean)
          .join(" · "),
      };
    default:
      return null;
  }
}
