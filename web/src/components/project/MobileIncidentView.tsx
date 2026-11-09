"use client";

import { useLiveServiceUrl } from "@/features/projects/liveDomain";
import { lastDeployedAt, serviceIssues } from "@/features/projects/facts";
import { useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import type { KusoAddon, KusoEnvironment, KusoService, KusoEnvVar } from "@/types/projects";
import { cn, serviceShortName } from "@/lib/utils";
import {
  buildTriggerMessage,
  deriveAddonStatus,
  deriveServiceStatus,
  serviceUrl,
  STATUS_TONE,
  useLatestBuilds,
  useTriggerBuild,
  useStopService,
  useStartService,
  useRestartService,
  useServiceEnv,
  type BuildSummary,
  type NodeStatus,
} from "@/features/services";
import { api } from "@/lib/api-client";
import { envGroupName, isProductionGroup } from "@/lib/env-group";
import { AddonIcon, addonLabel } from "@/components/addon/AddonIcon";
import { useCanOnProject, Perms } from "@/features/auth";
import { Button } from "@/components/ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import {
  ExternalLink,
  RotateCcw,
  ScrollText,
  Play,
  Square,
  ChevronDown,
  MoreHorizontal,
  RotateCw,
  ChevronRight,
  KeyRound,
  Lock,
  AlertTriangle,
} from "lucide-react";
import { toast } from "sonner";
import { toastError } from "@/lib/errors";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { relativeTime } from "@/lib/format";
import { humanizeNotification } from "@/lib/format-notification";
import { ErrorBadge, VulnBadge } from "@/components/canvas/NodeBadges";
import { useServiceBadges } from "@/components/canvas/useServiceBadges";
import type { ErrorSummary, VulnSummary } from "@/components/canvas/badgeSummary";
import { FirstDeployChecklist } from "./FirstDeployChecklist";
import { sortBySeverity } from "./listSort";
import { fetchProjectAlerts, groupRepeats, type FeedEvent } from "./projectAlerts";

interface Props {
  project: string;
  selectedEnv: string;
  services: KusoService[];
  addons: KusoAddon[];
  envs: KusoEnvironment[];
  onSelectService?: (svcName: string, tab?: string) => void;
  onSelectAddon?: (addonName: string, tab?: string) => void;
}

function envFor(envs: KusoEnvironment[], svc: KusoService): KusoEnvironment | undefined {
  const all = envs.filter((e) => e.spec.service === svc.metadata.name);
  return all.find((e) => isProductionGroup(e)) ?? all[0];
}

// MobileIncidentView is the phone-shaped surface for kuso: the app is
// desktop-shaped for authoring, but when something is on fire you have
// your phone, not your laptop. So this view is scoped to on-call needs:
// what fired (alerts feed), per-service status + build health, and the
// four actions you actually reach for at 2am — open the site, read
// logs, stop/start, and redeploy. Authoring stays on desktop behind the
// interstitial; this is triage, not configuration.
export function MobileIncidentView({
  project,
  selectedEnv,
  services,
  addons,
  envs,
  onSelectService,
  onSelectAddon,
}: Props) {
  // Same latest-builds query (and key) as the canvas, so the status
  // shown here matches the desktop node for the same service.
  const latestBuilds = useLatestBuilds(project, selectedEnv);
  const rows = sortBySeverity(
    services.map((svc) => {
      const env = envFor(envs, svc);
      const short = serviceShortName(project, svc.metadata.name);
      const build = latestBuilds.data?.[short];
      const status = deriveServiceStatus({ stopped: !!svc.spec.stopped, env, latestBuild: build });
      return { svc, env, short, build, status, name: svc.spec.displayName?.trim() || short };
    }),
  );
  const { vulnSummaries, errorSummaries } = useServiceBadges(project, selectedEnv, services);
  const live = rows.filter((r) => r.status.kind === "live").length;
  const issues = serviceIssues(rows.map((r) => r.status));

  return (
    <div className="space-y-3 p-4 sm:hidden">
      <header className="flex items-end justify-between gap-3 px-1">
        <div className="min-w-0">
          <h1 className="truncate font-heading text-lg font-semibold tracking-tight">{project}</h1>
          <p className="mt-0.5 text-sm text-[var(--text-secondary)]">
            {live} of {rows.length} {rows.length === 1 ? "service" : "services"} live
            {issues.text && (
              <span className={issues.tone === "error" ? "text-[var(--error)]" : "text-[var(--warning)]"}> · {issues.text}</span>
            )}
          </p>
        </div>
        <span className="shrink-0 font-mono text-xs text-[var(--text-tertiary)]">{selectedEnv}</span>
      </header>

      <FirstDeployChecklist
        project={project}
        selectedEnv={selectedEnv}
        services={services}
        envs={envs}
        onOpenService={onSelectService}
        className="rounded-xl border px-4"
      />

      <ProjectAlerts project={project} onSelectService={onSelectService} />

      {rows.map((r) => (
        <MobileServiceCard
          key={r.svc.metadata.name}
          project={project}
          service={r.svc}
          env={r.env}
          latest={r.build}
          status={r.status}
          errors={errorSummaries[r.short]}
          vulns={vulnSummaries.services[r.short]}
          onSelectService={onSelectService}
        />
      ))}

      {addons.length > 0 && (
        <section className="rounded-xl border border-[var(--border-subtle)] bg-[var(--bg-elevated)]">
          <h2 className="px-4 pb-1 pt-3 text-sm font-medium text-[var(--text-secondary)]">Addons</h2>
          <ul>
            {addons.map((a) => {
              const st = deriveAddonStatus(a);
              return (
                <li key={a.metadata.name} className="border-t border-[var(--border-subtle)] first:border-t-0">
                  <button
                    type="button"
                    onClick={() => onSelectAddon?.(a.metadata.name)}
                    className="flex min-h-12 w-full items-center gap-3 px-4 py-2.5 text-left"
                  >
                    <AddonIcon kind={a.spec.kind} className="h-5 w-5 shrink-0" />
                    <span className="min-w-0 flex-1">
                      <span className="block truncate text-sm font-medium">{serviceShortName(project, a.metadata.name)}</span>
                      <span className="block truncate text-xs text-[var(--text-tertiary)]">
                        {addonLabel(a.spec.kind)}
                        {a.spec.version ? ` ${a.spec.version}` : ""}
                      </span>
                    </span>
                    <StatusLabel status={st} />
                    <ChevronRight className="h-4 w-4 shrink-0 text-[var(--text-tertiary)]" aria-hidden />
                  </button>
                </li>
              );
            })}
          </ul>
        </section>
      )}

      <p className="px-1 pb-2 text-xs text-[var(--text-tertiary)]">
        This is the on-call view: status, logs, stop/start and redeploy. Open kuso on a larger screen for the canvas and
        full configuration.
      </p>
    </div>
  );
}

function StatusLabel({ status }: { status: NodeStatus }) {
  const tone = STATUS_TONE[status.tone];
  return (
    <span className="inline-flex shrink-0 items-center gap-1.5 text-xs" title={status.detail}>
      <span aria-hidden className={cn("h-2 w-2 rounded-full", tone.dot)} />
      <span className={status.tone === "muted" ? "text-[var(--text-secondary)]" : tone.text}>{status.label}</span>
    </span>
  );
}


// ProjectAlerts surfaces the recent notification feed for THIS project:
// "what fired" is the first question on-call asks. Collapsed to one line
// with a count by default so the service cards stay above the fold.
function ProjectAlerts({
  project,
  onSelectService,
}: {
  project: string;
  onSelectService?: (svcName: string, tab?: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const feed = useQuery<FeedEvent[]>({
    queryKey: ["notifications", "my-feed", "mobile", project],
    queryFn: () => fetchProjectAlerts(project, (p) => api<FeedEvent[]>(p)),
    // Poll while the view is open — an on-call user is watching for the
    // next event to land, not opening a stale snapshot.
    refetchInterval: 20_000,
    staleTime: 10_000,
    retry: false,
    throwOnError: false,
  });

  const events = feed.data ?? [];
  const problems = events.filter((e) => e.severity === "error" || e.severity === "warn");
  // The same alert firing repeatedly reads as one row "×N", newest first.
  const shown = groupRepeats(problems.length > 0 ? problems : events).slice(0, 6);
  const updated = feed.dataUpdatedAt ? relativeTime(new Date(feed.dataUpdatedAt).toISOString()) : undefined;
  const failed = feed.isError;

  let summary: ReactNode;
  if (failed) {
    summary = <span className="text-[var(--error)]">Unavailable</span>;
  } else if (feed.isPending) {
    summary = <span className="text-[var(--text-tertiary)]">Loading…</span>;
  } else if (problems.length > 0) {
    summary = (
      <span>
        <span className="font-medium tabular-nums text-[var(--text-primary)]">{problems.length}</span>{" "}
        {problems.length === 1 ? "problem" : "problems"}
        <span className="text-[var(--text-tertiary)]"> · latest {relativeTime(problems[0].createdAt)}</span>
      </span>
    );
  } else if (events.length > 0) {
    summary = (
      <span className="text-[var(--text-tertiary)]">
        No problems · {events.length} {events.length === 1 ? "event" : "events"}
      </span>
    );
  } else {
    summary = <span className="text-[var(--text-tertiary)]">Nothing recent</span>;
  }

  return (
    <section className="rounded-xl border border-[var(--border-subtle)] bg-[var(--bg-elevated)]">
      <div className="flex items-center gap-2 pr-2">
        <button
          type="button"
          aria-expanded={open}
          aria-controls={`alerts-${project}`}
          onClick={() => setOpen((v) => !v)}
          disabled={!failed && shown.length === 0}
          className="flex min-h-12 min-w-0 flex-1 items-center gap-2 px-4 py-2.5 text-left text-sm disabled:cursor-default"
        >
          <AlertTriangle
            className={cn(
              "h-4 w-4 shrink-0",
              problems.length > 0 && !failed ? "text-[var(--error)]" : "text-[var(--text-tertiary)]",
            )}
            aria-hidden
          />
          <span className="shrink-0 font-medium text-[var(--text-secondary)]">Recent alerts</span>
          <span className="min-w-0 truncate">{summary}</span>
          {(failed || shown.length > 0) && (
            <ChevronDown
              aria-hidden
              className={cn("ml-auto h-4 w-4 shrink-0 text-[var(--text-tertiary)] transition-transform", open && "rotate-180")}
            />
          )}
        </button>
        {failed && (
          <Button type="button" size="sm" variant="outline" className="h-9 shrink-0" onClick={() => void feed.refetch()}>
            Retry
          </Button>
        )}
      </div>
      {open && (
        <div id={`alerts-${project}`} className="border-t border-[var(--border-subtle)] px-4 pb-3 pt-2.5">
          {failed ? (
            <p className="text-xs text-[var(--text-secondary)]">
              {feed.error instanceof Error ? feed.error.message : "The alerts feed didn't answer."}
              {updated ? ` Last loaded ${updated}.` : ""}
            </p>
          ) : (
            <ul className="space-y-2.5">
              {shown.map(({ event: e, count }) => (
                <li key={e.id} className="flex items-start gap-2.5">
                  <span
                    className={`mt-1.5 h-1.5 w-1.5 shrink-0 rounded-full ${
                      e.severity === "error"
                        ? "bg-[var(--error)]"
                        : e.severity === "warn"
                          ? "bg-[var(--warning)]"
                          : "bg-[var(--text-tertiary)]"
                    }`}
                  />
                  <button
                    type="button"
                    onClick={() => e.service && onSelectService?.(serviceShortName(project, e.service), "logs")}
                    className="min-w-0 flex-1 text-left"
                  >
                    <AlertText event={e} />
                    <p className="mt-0.5 text-xs text-[var(--text-tertiary)]">
                      {relativeTime(e.createdAt)}
                      {count > 1 && (
                        <span className="ml-1.5 rounded bg-[var(--bg-tertiary)] px-1 font-medium tabular-nums text-[var(--text-secondary)]">
                          ×{count}
                        </span>
                      )}
                    </p>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
      {failed && updated && !open && (
        <p className="-mt-1.5 px-4 pb-2.5 text-xs text-[var(--text-tertiary)]">Last loaded {updated}</p>
      )}
    </section>
  );
}


// Feed text is written for chat (backticks, Go durations, build CR names);
// the shared humanizer turns it into dashboard copy.
function AlertText({ event }: { event: FeedEvent }) {
  const text = humanizeNotification(event);
  return (
    <>
      <p className="line-clamp-2 text-sm font-medium break-words">{text.title}</p>
      {text.body.length > 0 && (
        <p className="mt-0.5 line-clamp-2 whitespace-pre-line text-xs text-[var(--text-secondary)]">
          {text.body.map((p, i) =>
            p.code ? (
              <code key={i} className="rounded bg-[var(--bg-tertiary)] px-1 font-mono">
                {p.text}
              </code>
            ) : (
              <span key={i}>{p.text}</span>
            ),
          )}
        </p>
      )}
    </>
  );
}

function MobileServiceCard({
  project,
  service,
  env,
  latest,
  status,
  errors,
  vulns,
  onSelectService,
}: {
  project: string;
  service: KusoService;
  env?: KusoEnvironment;
  latest?: BuildSummary;
  status: NodeStatus;
  errors?: ErrorSummary;
  vulns?: VulnSummary;
  onSelectService?: (svcName: string, tab?: string) => void;
}) {
  const shortName = serviceShortName(project, service.metadata.name);
  const displayName = service.spec.displayName?.trim() || shortName;
  const trigger = useTriggerBuild(project, shortName);
  const stop = useStopService(project, shortName);
  const start = useStartService(project, shortName);
  const restart = useRestartService(project, shortName);
  const [envOpen, setEnvOpen] = useState(false);
  const [confirmingStop, setConfirmingStop] = useState(false);
  const [moreOpen, setMoreOpen] = useState(false);
  // Redeploy + stop/start are editor-level mutations; disable with a
  // hint for viewers instead of a post-tap 403 toast.
  const canWrite = useCanOnProject(project, Perms.ServicesWrite);

  const url = useLiveServiceUrl(project, shortName, env, serviceUrl(service, env));
  const stopped = service.spec.stopped === true;
  const deployed = lastDeployedAt(env);
  const latestFailed = ["failed", "error", "release-failed"].includes((latest?.status ?? "").toLowerCase());

  const redeploy = () => {
    // Build the branch this env group tracks; an empty body builds the
    // project's default branch, which on a staging tab redeploys production.
    trigger.mutate(
      env?.spec.branch ? { branch: env.spec.branch } : {},
      {
        onSuccess: (res) =>
          toast.success(buildTriggerMessage(res, `Redeploy queued for ${shortName}`, shortName)),
        onError: (err) => toastError(err, "Redeploy failed"),
      },
    );
  };

  const toggleStop = () => {
    const m = stopped ? start : stop;
    m.mutate(undefined, {
      onSuccess: () => toast.success(stopped ? `Starting ${shortName}` : `Stopping ${shortName}`),
      onError: (err) => toastError(err, "Action failed"),
      onSettled: () => setConfirmingStop(false),
    });
  };
  const toggling = stop.isPending || start.isPending;

  return (
    <article className="rounded-xl border border-[var(--border-subtle)] bg-[var(--bg-elevated)] p-4 shadow-[var(--shadow-sm)]">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <h3 className="truncate text-base font-semibold">{displayName}</h3>
          {(errors?.occurrences || vulns?.fixableCritical) ? (
            <div className="mt-1 flex flex-wrap items-center gap-1.5">
              <ErrorBadge summary={errors} onOpen={() => onSelectService?.(shortName, "errors")} />
              <VulnBadge summary={vulns} onOpen={() => onSelectService?.(shortName, "vulnerabilities")} />
            </div>
          ) : null}
          <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs">
            <StatusLabel status={status} />
            {deployed && <span className="text-[var(--text-tertiary)]">deployed {relativeTime(deployed)}</span>}
          </div>
          {status.detail && <p className="mt-1 text-xs text-[var(--text-secondary)]">{status.detail}</p>}
          {latestFailed && status.tone !== "error" && (
            <p className="mt-1 text-xs text-[var(--error)]">Latest build failed; the previous build is still serving.</p>
          )}
        </div>
        {url && (
          <a
            href={url}
            target="_blank"
            rel="noreferrer"
            className="inline-flex h-10 w-10 shrink-0 items-center justify-center rounded-md border border-[var(--border-subtle)] text-[var(--text-secondary)]"
            aria-label={`Open ${url.replace(/^https?:\/\//, "")} in a new tab`}
          >
            <ExternalLink className="h-4 w-4" />
          </a>
        )}
      </div>
      {url && (
        <p className="mt-2 truncate font-mono text-xs text-[var(--text-tertiary)]">{url.replace(/^https?:\/\//, "")}</p>
      )}

      {/* Logs is the inline action: on a phone you're reading what broke.
          Redeploy, Restart and Stop live in the overflow so a mutating
          button is never one stray tap away. */}
      <div className="mt-3 grid grid-cols-[1fr_auto_auto] gap-2 [&_button]:h-10">
        <Button type="button" variant="secondary" size="sm" onClick={() => onSelectService?.(shortName, "logs")}>
          <ScrollText className="mr-1.5 h-3.5 w-3.5" /> Logs
        </Button>
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={() => setEnvOpen((o) => !o)}
          aria-expanded={envOpen}
        >
          <KeyRound className="mr-1.5 h-3.5 w-3.5" /> Env
          <ChevronDown className={`ml-1 h-3.5 w-3.5 transition-transform ${envOpen ? "rotate-180" : ""}`} />
        </Button>
        <Popover open={moreOpen} onOpenChange={setMoreOpen}>
          <PopoverTrigger
            aria-label={`More actions for ${displayName}`}
            className="inline-flex h-10 w-10 items-center justify-center rounded-md border border-[var(--border-subtle)] text-[var(--text-secondary)] outline-none focus-visible:ring-2 focus-visible:ring-[var(--focus)] data-[popup-open]:bg-[var(--bg-tertiary)]"
          >
            <MoreHorizontal className="h-4 w-4" />
          </PopoverTrigger>
          <PopoverContent align="end" className="w-56 gap-0 p-1">
            <button
              type="button"
              className={menuItem}
              disabled={!canWrite || trigger.isPending}
              onClick={() => {
                setMoreOpen(false);
                redeploy();
              }}
            >
              <RotateCcw className="h-4 w-4 text-[var(--text-tertiary)]" />
              {trigger.isPending ? "Queuing…" : "Redeploy"}
              <span className="ml-auto text-xs text-[var(--text-tertiary)]">new build</span>
            </button>
            <button
              type="button"
              className={menuItem}
              disabled={!canWrite || stopped || restart.isPending}
              onClick={() => {
                setMoreOpen(false);
                restart.mutate(env ? envGroupName(env) : "production", {
                  onSuccess: () => toast.success(`Restarting ${shortName}`),
                  onError: (err) => toastError(err, "Restart failed"),
                });
              }}
            >
              <RotateCw className="h-4 w-4 text-[var(--text-tertiary)]" />
              Restart
              <span className="ml-auto text-xs text-[var(--text-tertiary)]">{stopped ? "start it first" : "same image"}</span>
            </button>
            <button
              type="button"
              className={cn(menuItem, !stopped && "text-[var(--error)]")}
              disabled={!canWrite || toggling}
              onClick={() => {
                setMoreOpen(false);
                // Start is one tap; Stop takes the service offline, so it confirms.
                if (stopped) toggleStop();
                else setConfirmingStop(true);
              }}
            >
              {stopped ? <Play className="h-4 w-4" /> : <Square className="h-4 w-4" />}
              {toggling ? "Working…" : stopped ? "Start" : "Stop…"}
            </button>
            {!canWrite && <p className="px-2 py-1.5 text-xs text-[var(--text-tertiary)]">Requires editor access on this project</p>}
          </PopoverContent>
        </Popover>
      </div>

      {envOpen && <MobileEnvReadout project={project} service={shortName} />}
      <ConfirmDialog
        open={confirmingStop}
        title={`Stop ${shortName}`}
        body="Scales the service to zero until you start it again. Public services return a 503 meanwhile; workers stop processing."
        confirmLabel="Stop service"
        destructive
        pending={stop.isPending}
        onConfirm={toggleStop}
        onCancel={() => setConfirmingStop(false)}
      />
    </article>
  );
}

// MobileEnvReadout is a READ-ONLY env dump for the "is a var set wrong?"
// question. It honours the server's masked flag: an editor without
// secrets:read gets names only, values hidden — the exact boundary the
// desktop editor enforces. Editing stays on desktop.
function MobileEnvReadout({ project, service }: { project: string; service: string }) {
  const q = useServiceEnv(project, service);
  const vars: KusoEnvVar[] = q.data?.envVars ?? [];
  const masked = q.data?.masked === true;

  return (
    <div className="mt-3 rounded-lg border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-3">
      {q.isLoading ? (
        <p className="text-xs text-[var(--text-tertiary)]">Loading env…</p>
      ) : vars.length === 0 ? (
        <p className="text-xs text-[var(--text-tertiary)]">No env vars.</p>
      ) : (
        <ul className="space-y-1.5">
          {masked && (
            <li className="mb-1 flex items-center gap-1.5 text-xs text-[var(--text-tertiary)]">
              <Lock className="h-3 w-3" aria-hidden /> Values hidden. Reading them needs secrets access.
            </li>
          )}
          {vars.map((v) => (
            <li key={v.name} className="min-w-0 font-mono text-xs leading-relaxed">
              <span className="text-[var(--text-secondary)]">{v.name}</span>
              <span className="text-[var(--text-tertiary)]">=</span>
              <span className="break-all text-[var(--text-primary)]">
                {v.valueFrom ? "<secret-ref>" : masked ? "••••••••" : (v.value ?? "")}
              </span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

const menuItem =
  "flex min-h-10 w-full items-center gap-2 rounded-md px-2 text-left text-sm outline-none hover:bg-[var(--bg-tertiary)] focus-visible:bg-[var(--bg-tertiary)] disabled:pointer-events-none disabled:opacity-50";
