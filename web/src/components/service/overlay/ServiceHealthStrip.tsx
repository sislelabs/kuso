"use client";

import { ApiError } from "@/lib/api-client";
import { relativeTime } from "@/lib/format";
import { TONE_CLASSES, type StatusMeta, type StatusTone } from "@/lib/status";
import { cn } from "@/lib/utils";
import { StatusDot } from "@/components/ui/status-pill";
import { ERRORS_WINDOW, errorCountLabel, errorsEnvScope, useServiceErrorCount } from "@/features/errors";
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
  // Never-run services collapse the strip into one banner.
  firstRun?: FirstRun;
  // A known failure's fix (Change branch, …): the banner's primary action.
  fix?: FixFirst;
  // The Deployments tab is open: its failed build row shows the reason.
  onDeployments?: boolean;
  onNavigate: (tab: string, anchor?: string) => void;
}

interface Cell {
  id: string;
  label: string;
  value: string;
  // Muted trailing text, e.g. "1/2 ready" next to the status.
  secondary?: { text: string; tone: StatusTone | "plain" };
  tone: StatusTone | "plain";
  title: string;
  tab: string;
  anchor?: string;
}

// ServiceHealthStrip is the at-a-glance row under the overlay title:
// status, uptime ping, restarts, errors and last deploy. Every cell reads
// the same query the tab it links to reads, so the numbers match.
export function ServiceHealthStrip(props: ServiceHealthStripProps) {
  if (props.firstRun?.kind === "never") {
    return <NeverRanBanner {...props} firstRun={props.firstRun} />;
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

function HealthCells({ project, service, env, envGroup, status, statusDetail, alert, onNavigate }: ServiceHealthStripProps) {
  const pods = useServicePods(project, service, env?.metadata?.name);
  const uptime = useProjectUptime(project);
  const errors = useServiceErrorCount(project, service, ERRORS_WINDOW, errorsEnvScope(envGroup));

  // Finished pods, a draining old ReplicaSet and release-hook pods aren't instances.
  const podList =
    pods.data?.pods?.filter((p) => isCurrentPod(p) && p.phase !== "Succeeded" && p.phase !== "Failed") ?? null;
  // Ready/desired from the env's rollup, the numbers the canvas and list
  // show; a pod list mid-rollout still holds the old ReplicaSet's pods.
  const replicas = env?.status?.replicas as { ready?: number; desired?: number } | undefined;
  const hasRollup = typeof replicas?.desired === "number";
  const ready = hasRollup ? (replicas?.ready ?? 0) : (podList?.filter((p) => p.ready).length ?? 0);
  const total = hasRollup ? (replicas?.desired ?? 0) : (podList?.length ?? 0);
  // While the status is a problem, "1/1 ready" or "Uptime: Up" next to it
  // reads as a contradiction; the cause takes their place.
  const problem = status.tone === "danger" || status.tone === "warning";
  const httpService = !env || !!env.spec.host || (env.spec.additionalHosts?.length ?? 0) > 0;
  const restarts = podList?.reduce((n, p) => n + (p.restarts ?? 0), 0) ?? 0;
  const crashing = podList?.some((p) => /crashloop/i.test(p.reason ?? "")) ?? false;
  const deployedAt = lastDeployedAt(env);

  const cells: Cell[] = [
    {
      id: "status",
      label: "Status",
      value: status.label,
      secondary:
        total > 0 && !problem
          ? { text: `${ready}/${total} ready`, tone: ready === total ? "plain" : ready === 0 ? "danger" : "warning" }
          : undefined,
      tone: status.tone,
      title: total > 0 ? `${ready} of ${total} instances ready` : status.label,
      tab: "deployments",
    },
    ...(problem
      ? alert
        ? []
        : [
            {
              id: "reason",
              label: "Reason",
              value: statusDetail || status.label,
              tone: status.tone,
              title: statusDetail || status.label,
              tab: "deployments",
            } satisfies Cell,
          ]
      : [
          uptimeCell(
            uptime.data?.enabled,
            pickUptime(uptime.data?.services ?? null, service, env, envGroup),
            uptime.error,
            httpService,
          ),
        ]),
    {
      id: "restarts",
      label: "Restarts",
      value: pods.isPending ? "…" : podList ? restarts.toLocaleString() : "—",
      tone: crashing ? "danger" : restarts > 0 ? "warning" : "plain",
      title: "Container restarts across the running instances, counted since each instance started. Open logs to see why.",
      tab: "logs",
    },
    {
      id: "errors",
      label: `Errors · ${ERRORS_WINDOW}`,
      value: errors.data ? errors.data.occurrences.toLocaleString() : errors.isError ? "—" : "…",
      tone: (errors.data?.occurrences ?? 0) > 0 ? "danger" : "plain",
      title: errors.data
        ? `${errorCountLabel(errors.data.occurrences)} in ${errors.data.groups} ${errors.data.groups === 1 ? "group" : "groups"}`
        : "Runtime errors in the last 24 hours",
      tab: "errors",
    },
    {
      id: "deploy",
      label: "Last deploy",
      value: deployedAt ? relativeTime(deployedAt) : "Never",
      tone: "plain",
      title: deployedAt ? `Current release went live ${new Date(deployedAt).toLocaleString()}` : "Nothing has been deployed to this environment",
      tab: "deployments",
    },
  ];

  const errorsN = errors.data?.occurrences;
  const deploy = cells.find((c) => c.id === "deploy");
  // 4 or 5 cells: fill the row at lg; two rows of 2 + 3 (or 2 + 2) below.
  const lgCols = cells.length === 5 ? "lg:grid-cols-5" : "lg:grid-cols-4";
  const span = (i: number) => (cells.length === 5 && i < 3 ? "col-span-2" : "col-span-3");
  const grid = (
    <ul
      aria-label="Service health"
      className={cn(
        "hidden grid-cols-6 gap-px bg-[var(--border-subtle)] sm:grid",
        lgCols,
        alert ? "border-t border-[var(--border-subtle)]" : "mt-3 overflow-hidden rounded-lg border border-[var(--border-subtle)]",
      )}
    >
      {cells.map((c, i) => (
        <li key={c.id} className={cn("flex min-w-0 bg-[var(--bg-primary)] lg:col-span-1", span(i))}>
          <button
            type="button"
            title={c.title}
            aria-label={`${c.label}: ${c.value}${c.secondary ? `, ${c.secondary.text}` : ""}. ${c.title}`}
            onClick={() => onNavigate(c.tab, c.anchor)}
            className={cn(
              "flex min-w-0 flex-1 text-left transition-colors hover:bg-[var(--bg-secondary)] focus-visible:outline-offset-[-2px]",
              // Folded under a problem banner the cells go single-line, so
              // the block costs one row of height instead of two.
              alert ? "items-baseline gap-2 px-3 py-1.5" : "flex-col items-start gap-0.5 px-3 py-2",
            )}
          >
            <span className="flex shrink-0 items-center gap-1.5 text-xs text-[var(--text-tertiary)]">
              {c.id === "status" && <StatusDot status={status} />}
              {c.label}
            </span>
            {/* The value never wraps mid-word; qualifiers go on the muted line below. */}
            <span
              className={cn(
                "max-w-full truncate whitespace-nowrap text-sm font-medium",
                c.tone === "plain" ? "text-[var(--text-primary)]" : TONE_CLASSES[c.tone].text,
              )}
            >
              {c.value}
            </span>
            {c.secondary && (
              <span
                className={cn(
                  "truncate text-xs font-normal",
                  c.secondary.tone === "plain" ? "text-[var(--text-secondary)]" : TONE_CLASSES[c.secondary.tone].text,
                )}
              >
                {c.secondary.text}
              </span>
            )}
          </button>
        </li>
      ))}
    </ul>
  );
  const phone = (
    <ul aria-label="Service health" className={cn("flex flex-wrap items-center gap-1.5 sm:hidden", alert ? "border-t border-[var(--border-subtle)] p-2" : "mt-2")}>
      <li>
        <button type="button" onClick={() => onNavigate("deployments")} className={phoneChip}>
          <StatusDot status={status} />
          <span className={cn("font-medium", TONE_CLASSES[status.tone].text)}>{status.label}</span>
          {total > 0 && !problem && <span className="whitespace-nowrap">· {ready}/{total} ready</span>}
        </button>
      </li>
      {errorsN !== undefined && errorsN > 0 && (
        <li>
          <button type="button" onClick={() => onNavigate("errors")} className={cn(phoneChip, "text-[var(--danger)]")}>
            {errorsN.toLocaleString()} {errorsN === 1 ? "error" : "errors"} · {ERRORS_WINDOW}
          </button>
        </li>
      )}
      {restarts > 0 && (
        <li>
          <button type="button" onClick={() => onNavigate("logs")} className={cn(phoneChip, crashing ? "text-[var(--danger)]" : "text-[var(--warning)]")}>
            {restarts.toLocaleString()} {restarts === 1 ? "restart" : "restarts"}
          </button>
        </li>
      )}
      {deploy && deploy.value !== "Never" && (
        <li className="min-w-0">
          <button type="button" onClick={() => onNavigate("deployments")} className={phoneChip}>
            <span className="truncate">deployed {deploy.value}</span>
          </button>
        </li>
      )}
    </ul>
  );
  if (!alert) {
    return (
      <>
        {/* Phones: one compact row, each fact its own tap target that opens
            the tab it came from. */}
        {phone}
        {grid}
      </>
    );
  }
  return (
    <div
      className={cn(
        "mt-3 overflow-hidden rounded-lg border",
        status.tone === "danger" ? "border-[var(--danger)]/35" : "border-[var(--warning)]/35",
      )}
    >
      {alert}
      {phone}
      {grid}
    </div>
  );
}

const phoneChip =
  "inline-flex min-h-11 max-w-full items-center gap-1.5 rounded-md border border-[var(--border-subtle)] px-2.5 text-xs text-[var(--text-secondary)] active:bg-[var(--bg-secondary)]";

// The checker reports one row per checked env, keyed by env CR name; it
// only checks production today, which is also what older servers meant
// when they sent no env.
function pickUptime(list: UptimeServiceStatus[] | null, service: string, env: KusoEnvironment | undefined, envGroup: string): UptimeServiceStatus | undefined {
  const mine = (list ?? []).filter((s) => s.service === service);
  const byName = env ? mine.find((s) => s.env === env.metadata.name) : undefined;
  return byName ?? (envGroup === "production" ? mine.find((s) => !s.env) : undefined);
}

function uptimeCell(
  enabled: boolean | undefined,
  s: UptimeServiceStatus | undefined,
  error: unknown,
  httpService: boolean,
): Cell {
  const base = { id: "uptime", label: "Uptime", tab: "settings", anchor: "uptime" } as const;
  if (!httpService) {
    return { ...base, value: "—", tone: "plain", title: "No public HTTP endpoint, so there's nothing for the uptime check to ping" };
  }
  if (error instanceof ApiError && error.status === 404) {
    return { ...base, value: "Unavailable", tone: "plain", title: "This server doesn't report uptime checks" };
  }
  if (enabled === false) {
    return { ...base, value: "Checker off", tone: "neutral", title: "Uptime checks are switched off for this kuso instance" };
  }
  const v = uptimeView(s);
  // "(last known)" is a qualifier, not part of the value.
  const value = v.stale ? v.label.replace(/\s*\(last known\)$/, "") : v.label;
  const staleNote = v.stale ? { text: "last known", tone: "plain" as const } : undefined;
  const checked = s?.lastCheckedAt ? `checked ${relativeTime(s.lastCheckedAt)}` : "not checked yet";
  switch (v.state) {
    case "not_set_up":
      return { ...base, value: v.label, tone: "neutral", title: "No uptime check for this environment. Set one up in Settings → Uptime." };
    case "up":
      return {
        ...base,
        value,
        secondary: staleNote,
        tone: "success",
        title: `${s?.url ?? ""} answered${s?.statusCode ? ` HTTP ${s.statusCode}` : ""}, ${checked}`,
      };
    case "failing":
      return { ...base, value, secondary: staleNote, tone: "warning", title: [s?.reason || s?.error, checked].filter(Boolean).join(" · ") };
    case "down":
      return {
        ...base,
        value,
        secondary: s?.since
          ? { text: `for ${relativeTime(s.since).replace(/ ago$/, "")}${v.stale ? " · last known" : ""}`, tone: "plain" }
          : staleNote,
        tone: "danger",
        title: [s?.reason || s?.error, s?.since ? `down since ${new Date(s.since).toLocaleString()}` : "", checked]
          .filter(Boolean)
          .join(" · "),
      };
    case "paused":
      return {
        ...base,
        value: v.label,
        tone: "neutral",
        title: s?.state === "disabled" ? "The uptime check for this service is off" : "Checks pause while the service sleeps, is stopped or has nothing deployed",
      };
    default:
      return { ...base, value: v.label, tone: "neutral", title: s?.url ?? "" };
  }
}
