"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import type { KusoEnvironment, KusoService, KusoEnvVar } from "@/types/projects";
import { serviceShortName } from "@/lib/utils";
import {
  useTriggerBuild,
  useStopService,
  useStartService,
  useServiceEnv,
  useBuilds,
} from "@/features/services";
import { api } from "@/lib/api-client";
import { Button } from "@/components/ui/button";
import {
  ExternalLink,
  RotateCcw,
  ScrollText,
  Play,
  Square,
  ChevronDown,
  KeyRound,
  Lock,
  AlertTriangle,
  BellOff,
} from "lucide-react";
import { toast } from "sonner";

interface Props {
  project: string;
  services: KusoService[];
  envs: KusoEnvironment[];
  onSelectService?: (svcName: string, tab?: string) => void;
}

// MobileIncidentView is the phone-shaped surface for kuso: the app is
// desktop-shaped for authoring, but when something is on fire you have
// your phone, not your laptop. So this view is scoped to on-call needs:
// what fired (alerts feed), per-service status + build health, and the
// four actions you actually reach for at 2am — open the site, read
// logs, stop/start, and redeploy. Authoring stays on desktop behind the
// interstitial; this is triage, not configuration.
export function MobileIncidentView({ project, services, envs, onSelectService }: Props) {
  return (
    <div className="space-y-3 p-3 sm:hidden">
      <div className="rounded-xl border border-[var(--border-subtle)] bg-[var(--bg-elevated)] p-4">
        <h2 className="font-heading text-base font-semibold tracking-tight">Incident mode</h2>
        <p className="mt-1 text-xs text-[var(--text-secondary)]">
          The controls you need during an outage: what fired, per-service status, logs, stop/start,
          and redeploy. Full configuration lives on desktop.
        </p>
      </div>

      <ProjectAlerts project={project} onSelectService={onSelectService} />

      {services.map((svc) => (
        <MobileServiceCard
          key={svc.metadata.name}
          project={project}
          service={svc}
          env={envs.find((e) => e.spec.service === svc.metadata.name && e.spec.kind === "production")}
          onSelectService={onSelectService}
        />
      ))}
    </div>
  );
}

// Minimal feed shape — mirrors the fields TopNav's bell popover reads.
// Kept local so the incident view doesn't couple to TopNav internals.
interface FeedEvent {
  id: number;
  type: string;
  title: string;
  body?: string;
  severity?: string;
  project?: string;
  service?: string;
  createdAt: string;
}

// ProjectAlerts surfaces the recent notification feed filtered to THIS
// project — "what fired" is the first question on-call asks. Uses the
// my-feed path (scoped, available to every role; admins could see more
// via /feed but the scoped view is the right default for triage).
function ProjectAlerts({
  project,
  onSelectService,
}: {
  project: string;
  onSelectService?: (svcName: string, tab?: string) => void;
}) {
  const feed = useQuery<FeedEvent[]>({
    queryKey: ["notifications", "my-feed", "mobile", project],
    queryFn: () => api("/api/notifications/my-feed?limit=30"),
    // Poll while the view is open — an on-call user is watching for the
    // next event to land, not opening a stale snapshot.
    refetchInterval: 20_000,
    staleTime: 10_000,
    retry: false,
    throwOnError: false,
  });

  const events = (feed.data ?? []).filter((e) => e.project === project).slice(0, 6);
  const problems = events.filter((e) => e.severity === "error" || e.severity === "warn");
  const shown = problems.length > 0 ? problems : events;

  return (
    <section className="rounded-xl border border-[var(--border-subtle)] bg-[var(--bg-elevated)] p-4">
      <div className="flex items-center gap-2">
        <AlertTriangle className="h-3.5 w-3.5 text-[var(--text-tertiary)]" />
        <h3 className="font-heading text-xs font-semibold uppercase tracking-widest text-[var(--text-tertiary)]">
          Recent alerts
        </h3>
      </div>
      {shown.length === 0 ? (
        <div className="mt-3 flex items-center gap-2 text-xs text-[var(--text-tertiary)]">
          <BellOff className="h-3.5 w-3.5" />
          {feed.isLoading ? "Loading…" : "Nothing recent for this project."}
        </div>
      ) : (
        <ul className="mt-3 space-y-2.5">
          {shown.map((e) => (
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
                <p className="truncate text-[12px] font-medium">{e.title}</p>
                {e.body && (
                  <p className="mt-0.5 line-clamp-2 text-[11px] text-[var(--text-secondary)]">{e.body}</p>
                )}
                <p className="mt-0.5 font-mono text-[10px] uppercase tracking-wider text-[var(--text-tertiary)]">
                  {relativeTime(e.createdAt)}
                </p>
              </button>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function MobileServiceCard({
  project,
  service,
  env,
  onSelectService,
}: {
  project: string;
  service: KusoService;
  env?: KusoEnvironment;
  onSelectService?: (svcName: string, tab?: string) => void;
}) {
  const shortName = serviceShortName(project, service.metadata.name);
  const trigger = useTriggerBuild(project, shortName);
  const stop = useStopService(project, shortName);
  const start = useStartService(project, shortName);
  const builds = useBuilds(project, shortName);
  const [envOpen, setEnvOpen] = useState(false);

  const url = env?.status?.url || service.spec.domains?.[0];
  const phase = env?.status?.phase || "unknown";
  const stopped = service.spec.stopped === true;
  const latest = builds.data?.[0];

  const redeploy = () => {
    trigger.mutate(
      {},
      {
        onSuccess: () => toast.success(`Redeploy queued for ${shortName}`),
        onError: (err) => toast.error(err instanceof Error ? err.message : "Redeploy failed"),
      },
    );
  };

  const toggleStop = () => {
    const m = stopped ? start : stop;
    m.mutate(undefined, {
      onSuccess: () => toast.success(stopped ? `Starting ${shortName}` : `Stopping ${shortName}`),
      onError: (err) => toast.error(err instanceof Error ? err.message : "Action failed"),
    });
  };
  const toggling = stop.isPending || start.isPending;

  return (
    <article className="rounded-xl border border-[var(--border-subtle)] bg-[var(--bg-elevated)] p-4 shadow-[var(--shadow-sm)]">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <h3 className="truncate font-heading text-sm font-semibold">{shortName}</h3>
          <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
            <StatusChip label={stopped ? "stopped" : phase} tone={statusTone(stopped ? "stopped" : phase)} />
            {latest && <StatusChip label={`build ${latest.status}`} tone={statusTone(latest.status)} />}
          </div>
        </div>
        {url && (
          <a
            href={String(url).startsWith("http") ? String(url) : `https://${url}`}
            target="_blank"
            rel="noreferrer"
            className="shrink-0 rounded-md border border-[var(--border-subtle)] p-2 text-[var(--text-secondary)]"
            aria-label="Open service"
          >
            <ExternalLink className="h-4 w-4" />
          </a>
        )}
      </div>

      <div className="mt-4 grid grid-cols-2 gap-2">
        <Button type="button" variant="outline" size="sm" onClick={() => onSelectService?.(shortName, "logs")}>
          <ScrollText className="mr-1.5 h-3.5 w-3.5" /> Logs
        </Button>
        <Button type="button" size="sm" onClick={redeploy} disabled={trigger.isPending}>
          <RotateCcw className="mr-1.5 h-3.5 w-3.5" />
          {trigger.isPending ? "Queuing" : "Redeploy"}
        </Button>
        <Button
          type="button"
          variant={stopped ? "default" : "outline"}
          size="sm"
          onClick={toggleStop}
          disabled={toggling}
          className={stopped ? "" : "text-[var(--error)]"}
        >
          {stopped ? (
            <Play className="mr-1.5 h-3.5 w-3.5" />
          ) : (
            <Square className="mr-1.5 h-3.5 w-3.5" />
          )}
          {toggling ? "Working" : stopped ? "Start" : "Stop"}
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
      </div>

      {envOpen && <MobileEnvReadout project={project} service={shortName} />}
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
            <li className="mb-1 flex items-center gap-1.5 text-[10px] uppercase tracking-wider text-[var(--text-tertiary)]">
              <Lock className="h-3 w-3" /> values hidden — read on desktop with secrets access
            </li>
          )}
          {vars.map((v) => (
            <li key={v.name} className="min-w-0 font-mono text-[11px] leading-relaxed">
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

function StatusChip({ label, tone }: { label: string; tone: "ok" | "warn" | "danger" | "muted" }) {
  const cls = {
    ok: "border-[var(--success)]/30 text-[var(--success)]",
    warn: "border-[var(--warning)]/30 text-[var(--warning)]",
    danger: "border-[var(--error)]/30 text-[var(--error)]",
    muted: "border-[var(--border-subtle)] text-[var(--text-tertiary)]",
  }[tone];
  return (
    <span
      className={`rounded border px-1.5 py-0.5 font-mono text-[10px] uppercase tracking-widest ${cls}`}
    >
      {label}
    </span>
  );
}

function statusTone(s: string): "ok" | "warn" | "danger" | "muted" {
  const v = s.toLowerCase();
  if (v.includes("run") && !v.includes("running")) return "warn"; // building/queued-ish
  if (["running", "ready", "active", "succeeded", "healthy"].some((k) => v.includes(k))) return "ok";
  if (["fail", "error", "crash", "stopped"].some((k) => v.includes(k))) return "danger";
  if (["pending", "queued", "building", "progress", "unknown"].some((k) => v.includes(k))) return "warn";
  return "muted";
}

// relativeTime renders a compact "3m"/"2h"/"5d" ago string. Kept local
// and dependency-free — the incident view shouldn't pull a date lib.
function relativeTime(iso: string): string {
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return "";
  const s = Math.max(0, Math.floor((Date.now() - then) / 1000));
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.floor(h / 24)}d ago`;
}
