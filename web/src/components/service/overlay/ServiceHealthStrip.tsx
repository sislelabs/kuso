"use client";

import { ApiError } from "@/lib/api-client";
import { relativeTime } from "@/lib/format";
import { SERVICE_STATUS, TONE_CLASSES, type StatusMeta, type StatusTone } from "@/lib/status";
import { cn } from "@/lib/utils";
import { StatusDot } from "@/components/ui/status-pill";
import { ERRORS_WINDOW, errorCountLabel, errorsEnvScope, useServiceErrorCount } from "@/features/errors";
import { useServicePods } from "@/features/pods";
import { lastDeployedAt } from "@/features/projects/facts";
import { useProjectUptime, uptimeView, type UptimeServiceStatus } from "@/features/uptime";
import type { KusoEnvironment } from "@/types/projects";

export interface ServiceHealthStripProps {
  project: string;
  service: string;
  env: KusoEnvironment | undefined;
  // Env group short name ("production", "staging", "preview-pr-7").
  envGroup: string;
  // The same status the header pill shows, so the two never disagree.
  status: StatusMeta;
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
// status, uptime check, restarts, errors and last deploy. Every cell reads
// the same query the tab it links to reads, so the numbers match.
export function ServiceHealthStrip({ project, service, env, envGroup, status: headerStatus, onNavigate }: ServiceHealthStripProps) {
  const pods = useServicePods(project, service, env?.metadata?.name);
  const uptime = useProjectUptime(project);
  const errors = useServiceErrorCount(project, service, ERRORS_WINDOW, errorsEnvScope(envGroup));

  // Finished pods (old ReplicaSet, completed jobs) aren't instances.
  const podList = pods.data?.pods?.filter((p) => p.phase !== "Succeeded" && p.phase !== "Failed") ?? null;
  // Ready/desired from the env's rollup, the numbers the canvas and list
  // show; a pod list mid-rollout still holds the old ReplicaSet's pods.
  const replicas = env?.status?.replicas as { ready?: number; desired?: number } | undefined;
  const hasRollup = typeof replicas?.desired === "number";
  const ready = hasRollup ? (replicas?.ready ?? 0) : (podList?.filter((p) => p.ready).length ?? 0);
  const total = hasRollup ? (replicas?.desired ?? 0) : (podList?.length ?? 0);
  // Serving, but not on every replica: Degraded, never Live.
  const status = headerStatus.tone === "success" && total > 0 && ready < total ? SERVICE_STATUS.degraded : headerStatus;
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
        total > 0
          ? { text: `${ready}/${total} ready`, tone: ready === total ? "plain" : ready === 0 ? "danger" : "warning" }
          : undefined,
      tone: status.tone,
      title: total > 0 ? `${ready} of ${total} instances ready` : status.label,
      tab: "deployments",
    },
    uptimeCell(
      uptime.data?.enabled,
      pickUptime(uptime.data?.services ?? null, service, env, envGroup),
      uptime.error,
      httpService,
    ),
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
      label: "Errors",
      value: errors.data ? errorCountLabel(errors.data.occurrences) : errors.isError ? "—" : "…",
      tone: (errors.data?.occurrences ?? 0) > 0 ? "danger" : "plain",
      title: errors.data
        ? `${errorCountLabel(errors.data.occurrences)} in ${errors.data.groups} ${errors.data.groups === 1 ? "group" : "groups"}`
        : "Runtime errors in the last 24 hours",
      tab: "errors",
    },
    {
      id: "deploy",
      label: "Last deploy",
      value: deployedAt ? relativeTime(deployedAt) : "Not deployed yet",
      tone: "plain",
      title: deployedAt ? `Current release went live ${new Date(deployedAt).toLocaleString()}` : "Nothing has been deployed to this environment",
      tab: "deployments",
    },
  ];

  return (
    <ul
      aria-label="Service health"
      className="mt-3 grid grid-cols-6 gap-px overflow-hidden rounded-lg border border-[var(--border-subtle)] bg-[var(--border-subtle)] lg:grid-cols-5"
    >
      {cells.map((c, i) => (
        <li key={c.id} className={cn("flex min-w-0 bg-[var(--bg-primary)] lg:col-span-1", i < 3 ? "col-span-2" : "col-span-3")}>
          <button
            type="button"
            title={c.title}
            aria-label={`${c.label}: ${c.value}${c.secondary ? `, ${c.secondary.text}` : ""}. ${c.title}`}
            onClick={() => onNavigate(c.tab, c.anchor)}
            className="flex min-w-0 flex-1 flex-col items-start gap-0.5 px-3 py-2 text-left transition-colors hover:bg-[var(--bg-secondary)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--focus)]"
          >
            <span className="text-xs text-[var(--text-tertiary)]">{c.label}</span>
            {/* Value and its note stack and wrap; never an ellipsis. */}
            <span
              className={cn(
                "flex min-w-0 max-w-full items-start gap-1.5 text-sm font-medium break-words",
                c.tone === "plain" ? "text-[var(--text-primary)]" : TONE_CLASSES[c.tone].text,
              )}
            >
              {c.id === "status" && <StatusDot status={status} className="mt-1.5" />}
              <span className="min-w-0">{c.value}</span>
            </span>
            {c.secondary && (
              <span
                className={cn(
                  "text-xs font-normal",
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
}

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
  const base = { id: "uptime", label: "Health check", tab: "settings", anchor: "uptime" } as const;
  if (!httpService) {
    return { ...base, value: "—", tone: "plain", title: "No HTTP port, so there's nothing for the health check to probe" };
  }
  if (error instanceof ApiError && error.status === 404) {
    return { ...base, value: "Unavailable", tone: "plain", title: "This server doesn't report health checks" };
  }
  if (enabled === false) {
    return { ...base, value: "Checker off", tone: "neutral", title: "Health checks are switched off for this kuso instance" };
  }
  const v = uptimeView(s);
  const checked = s?.lastCheckedAt ? `checked ${relativeTime(s.lastCheckedAt)}` : "not checked yet";
  switch (v.state) {
    case "not_set_up":
      return { ...base, value: v.label, tone: "neutral", title: "No health check for this environment. Set one up in Settings → Uptime." };
    case "up":
      return {
        ...base,
        value: v.label,
        tone: "success",
        title: `${s?.url ?? ""} answered${s?.statusCode ? ` HTTP ${s.statusCode}` : ""}, ${checked}`,
      };
    case "failing":
      return { ...base, value: v.label, tone: "warning", title: [s?.reason || s?.error, checked].filter(Boolean).join(" · ") };
    case "down":
      return {
        ...base,
        value: `${v.label}${s?.since ? ` · ${relativeTime(s.since).replace(/ ago$/, "")}` : ""}`,
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
        title: s?.state === "disabled" ? "The health check for this service is disabled" : "Checks pause while the service sleeps, is stopped or has nothing deployed",
      };
    default:
      return { ...base, value: v.label, tone: "neutral", title: s?.url ?? "" };
  }
}
