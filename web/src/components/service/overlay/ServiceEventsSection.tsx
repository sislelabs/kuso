"use client";

import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, Info } from "lucide-react";
import { api, ApiError } from "@/lib/api-client";
import { relativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import { Skeleton } from "@/components/ui/skeleton";

// GET /api/projects/{p}/services/{s}/events?env=<env CR name>
//   → { namespace, events: [{ type, reason, message, summary?, object,
//        count, firstSeen?, lastSeen }] }   (projects.PodEventList)
// Recent kube events for the service's current pods and its Deployment,
// newest first. Older servers 404; the section then points at the
// cluster-wide events list on Settings → Health.
export interface ServicePodEvent {
  type: string; // "Normal" | "Warning"
  reason: string;
  // Raw kube text.
  message: string;
  // message in plain words; only set where the raw text is unreadable
  // (FailedScheduling).
  summary?: string;
  object: string; // "pod/<name>" or "deployment/<name>"
  count: number;
  firstSeen?: string;
  lastSeen?: string;
}

async function listServiceEvents(project: string, service: string, env: string): Promise<ServicePodEvent[]> {
  const qs = env ? `?env=${encodeURIComponent(env)}` : "";
  const res = await api<{ namespace?: string; events?: ServicePodEvent[] | null }>(
    `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/events${qs}`,
  );
  return res.events ?? [];
}

const SHOWN = 12;

// ServiceEventsSection shows why pods are (not) starting, inline on the
// Deployments tab. The header's "View events" jumps here (#pod-events).
export function ServiceEventsSection({
  project,
  service,
  envName,
  canSeeClusterEvents,
}: {
  project: string;
  service: string;
  envName?: string;
  canSeeClusterEvents: boolean;
}) {
  const q = useQuery({
    queryKey: ["service-events", project, service, envName ?? ""],
    queryFn: () => listServiceEvents(project, service, envName ?? ""),
    enabled: !!envName,
    refetchInterval: 15_000,
    retry: (n, e) => !(e instanceof ApiError && (e.status === 404 || e.status === 403)) && n < 2,
  });
  const unsupported = q.error instanceof ApiError && q.error.status === 404;

  if (!envName) return null;
  const events = q.data ?? [];
  const warnings = events.filter((e) => e.type === "Warning").length;

  return (
    <section
      id="pod-events"
      aria-labelledby="pod-events-h"
      className="scroll-mt-2 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]"
    >
      <header className="flex items-center justify-between gap-2 border-b border-[var(--border-subtle)] px-3 py-2">
        <h3 id="pod-events-h" className="text-xs font-medium text-[var(--text-tertiary)]">
          Pod events
          {warnings > 0 && (
            <span className="ml-1.5 font-normal text-[var(--warning)]">
              {warnings} {warnings === 1 ? "warning" : "warnings"}
            </span>
          )}
        </h3>
        {canSeeClusterEvents && (
          <Link href="/settings/health#kube-events" className="text-xs text-[var(--text-secondary)] underline-offset-2 hover:underline">
            All cluster events
          </Link>
        )}
      </header>
      {unsupported ? (
        <p className="px-3 py-3 text-xs text-[var(--text-secondary)]">
          This server doesn&apos;t list events per service yet.{" "}
          {canSeeClusterEvents ? (
            <Link href="/settings/health#kube-events" className="font-medium text-[var(--text-primary)] underline underline-offset-2">
              View cluster events
            </Link>
          ) : (
            "Check the logs for why pods aren't starting."
          )}
        </p>
      ) : q.isPending ? (
        <div className="p-3">
          <Skeleton className="h-8 w-full" />
        </div>
      ) : q.isError ? (
        <p className="px-3 py-3 text-xs text-[var(--text-secondary)]">
          Couldn&apos;t load events.{" "}
          <button type="button" onClick={() => void q.refetch()} className="underline underline-offset-2">
            Try again
          </button>
        </p>
      ) : events.length === 0 ? (
        <p className="px-3 py-3 text-xs text-[var(--text-tertiary)]">No recent events for this service&apos;s pods.</p>
      ) : (
        <ul className="divide-y divide-[var(--border-subtle)]">
          {events.slice(0, SHOWN).map((e, i) => {
            const warn = e.type === "Warning";
            const Icon = warn ? AlertTriangle : Info;
            return (
              <li key={`${e.object}-${e.reason}-${e.lastSeen ?? e.firstSeen ?? ""}-${i}`} className="flex gap-2 px-3 py-2 text-xs">
                <Icon aria-hidden className={cn("mt-0.5 h-3.5 w-3.5 shrink-0", warn ? "text-[var(--warning)]" : "text-[var(--text-tertiary)]")} />
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-baseline gap-x-1.5">
                    <span className={cn("font-medium", warn ? "text-[var(--warning)]" : "text-[var(--text-primary)]")}>{e.reason}</span>
                    {e.count > 1 && <span className="text-[var(--text-tertiary)]">×{e.count}</span>}
                    <span className="truncate font-mono text-[var(--text-tertiary)]" title={e.object}>
                      {e.object}
                    </span>
                    {(e.lastSeen || e.firstSeen) && (
                      <time
                        dateTime={e.lastSeen || e.firstSeen}
                        title={eventWhen(e)}
                        className="ml-auto text-[var(--text-tertiary)]"
                      >
                        {relativeTime(e.lastSeen || e.firstSeen || "")}
                      </time>
                    )}
                  </div>
                  {e.summary ? (
                    <>
                      <p className="mt-0.5 break-words text-[var(--text-primary)]">{e.summary}</p>
                      {e.message && e.message !== e.summary && (
                        <details className="mt-0.5">
                          <summary className="cursor-pointer text-[var(--text-tertiary)] hover:text-[var(--text-secondary)]">
                            Details
                          </summary>
                          <p className="mt-0.5 break-words font-mono text-[var(--text-secondary)]">{e.message}</p>
                        </details>
                      )}
                    </>
                  ) : (
                    <p className="mt-0.5 break-words text-[var(--text-secondary)]">{e.message}</p>
                  )}
                </div>
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}

// Repeated events say when they started as well as when they last fired.
function eventWhen(e: ServicePodEvent): string {
  const last = e.lastSeen ? new Date(e.lastSeen).toLocaleString() : "";
  const first = e.firstSeen ? new Date(e.firstSeen).toLocaleString() : "";
  if (e.count > 1 && first && last && first !== last) return `First ${first}, last ${last}`;
  return last || first;
}
