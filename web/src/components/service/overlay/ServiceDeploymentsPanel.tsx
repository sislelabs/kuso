"use client";

import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { LogStream } from "@/components/logs/LogStream";
import { useBuilds, useTriggerBuild } from "@/features/services";
import { useCan, Perms } from "@/features/auth";
import type { BuildSummary } from "@/features/services/api";
import type { KusoEnvironment } from "@/types/projects";
import { relativeTime } from "@/lib/format";
import { ChevronDown, ChevronRight, RotateCcw, ExternalLink } from "lucide-react";
import { toast } from "sonner";
import { cn } from "@/lib/utils";

interface Props {
  project: string;
  service: string;
  env?: KusoEnvironment;
}

// Status drives the badge. ACTIVE is now reserved for the build whose
// image is the one the env is currently running — older successful
// builds become SUPERSEDED so the user can tell which one's live at
// a glance. Without this, every successful build wore an ACTIVE pill
// forever, which lied during a redeploy.
type Status = "active" | "superseded" | "failed" | "running" | "pending" | "unknown";

function classify(b: BuildSummary, activeImageTag?: string): Status {
  const s = (b.status ?? "").toLowerCase();
  if (s === "succeeded") {
    // No env tag yet (fresh service, never deployed) → first
    // successful build is the one that promoted, so it's active.
    if (!activeImageTag) return "active";
    return b.imageTag && b.imageTag === activeImageTag ? "active" : "superseded";
  }
  if (s === "failed") return "failed";
  if (s === "running") return "running";
  if (s === "pending") return "pending";
  return "unknown";
}

function statusBadge(s: Status) {
  const map: Record<Status, { label: string; cls: string }> = {
    active:     { label: "ACTIVE",     cls: "bg-emerald-500/10 text-emerald-400 border-emerald-500/30" },
    superseded: { label: "SUPERSEDED", cls: "bg-[var(--bg-tertiary)] text-[var(--text-tertiary)] border-[var(--border-subtle)]" },
    failed:     { label: "FAILED",     cls: "bg-red-500/10 text-red-400 border-red-500/30" },
    running:    { label: "BUILDING",   cls: "bg-amber-500/10 text-amber-400 border-amber-500/30" },
    pending:    { label: "PENDING",    cls: "bg-[var(--bg-tertiary)] text-[var(--text-secondary)] border-[var(--border-subtle)]" },
    unknown:    { label: "UNKNOWN",    cls: "bg-[var(--bg-tertiary)] text-[var(--text-tertiary)] border-[var(--border-subtle)]" },
  };
  const m = map[s];
  return (
    <span
      className={cn(
        "inline-flex shrink-0 items-center rounded px-1.5 py-0.5 font-mono text-[9px] font-semibold tracking-widest border",
        m.cls
      )}
    >
      {m.label}
    </span>
  );
}

export function ServiceDeploymentsPanel({ project, service, env }: Props) {
  const builds = useBuilds(project, service);
  const trigger = useTriggerBuild(project, service);
  const [expanded, setExpanded] = useState<string | null>(null);
  const canDeploy = useCan(Perms.ServicesWrite);

  const onRedeploy = async () => {
    try {
      await trigger.mutateAsync({});
      toast.success("Build triggered");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to trigger build");
    }
  };

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-3 text-xs text-[var(--text-secondary)]">
          {env?.status?.url ? (
            <a
              href={env.status.url as string}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1 font-mono text-[var(--accent)] hover:underline"
            >
              {(env.status.url as string).replace(/^https?:\/\//, "")}
              <ExternalLink className="h-3 w-3" />
            </a>
          ) : (
            <span className="font-mono text-[var(--text-tertiary)]">no URL yet</span>
          )}
          {env?.spec.kind && (
            <span className="font-mono text-[var(--text-tertiary)]">{env.spec.kind}</span>
          )}
        </div>
        {canDeploy ? (
          <Button size="sm" onClick={onRedeploy} disabled={trigger.isPending}>
            <RotateCcw className="h-3.5 w-3.5" />
            {trigger.isPending ? "Triggering…" : "Redeploy"}
          </Button>
        ) : (
          <span
            className="font-mono text-[10px] text-[var(--text-tertiary)]"
            title="services:write permission required"
          >
            read-only
          </span>
        )}
      </div>

      {builds.isPending ? (
        <div className="space-y-2">
          <Skeleton className="h-16 w-full" />
          <Skeleton className="h-16 w-full" />
          <Skeleton className="h-16 w-full" />
        </div>
      ) : (builds.data ?? []).length === 0 ? (
        <p className="rounded-md border border-dashed border-[var(--border-subtle)] p-6 text-center text-sm text-[var(--text-tertiary)]">
          No builds yet. Trigger one with the button above or push to the connected branch.
        </p>
      ) : (
        <ul className="space-y-2">
          {(() => {
            // env.spec.image.tag is the source of truth for "what's
            // actually running." Find the active build by matching
            // imageTag; null when the env hasn't been promoted yet.
            const envImage = (env?.spec as { image?: { tag?: string } } | undefined)?.image;
            const activeTag = envImage?.tag;
            return (builds.data ?? []).map((b) => {
              const s = classify(b, activeTag);
              const sha = (b.commitSha ?? "").slice(0, 12);
              const branch = b.branch ?? "—";
              const ts = b.startedAt ?? b.finishedAt;
              const created = ts ? relativeTime(ts) : "—";
              const isOpen = expanded === b.id;
              return (
                <li
                  key={b.id}
                  className={cn(
                    // overflow-hidden is the fix for the redeploy
                    // layout break — the expanded BuildLogs container
                    // contains a <pre> that grows wider than its
                    // parent and was punching through the rounded card.
                    "overflow-hidden rounded-md border bg-[var(--bg-secondary)]",
                    s === "failed" && "border-red-500/30",
                    s === "active" && "border-emerald-500/30",
                    s === "running" && "border-amber-500/30",
                    !["failed", "active", "running"].includes(s) && "border-[var(--border-subtle)]"
                  )}
                >
                  <button
                    type="button"
                    onClick={() => setExpanded(isOpen ? null : b.id)}
                    className="flex w-full items-center gap-3 px-3 py-2.5 text-left"
                  >
                    {statusBadge(s)}
                    <div className="min-w-0 flex-1">
                      <div className="truncate text-sm font-medium">
                        <span className="font-mono">{sha || "—"}</span>
                        <span className="ml-2 text-xs text-[var(--text-tertiary)]">on {branch}</span>
                      </div>
                      <div className="font-mono text-[10px] text-[var(--text-tertiary)]">{created}</div>
                    </div>
                    {isOpen ? (
                      <ChevronDown className="h-4 w-4 shrink-0 text-[var(--text-tertiary)]" />
                    ) : (
                      <ChevronRight className="h-4 w-4 shrink-0 text-[var(--text-tertiary)]" />
                    )}
                  </button>
                  {isOpen && (
                    <div className="min-w-0 border-t border-[var(--border-subtle)] bg-[var(--bg-primary)]">
                      <BuildLogs project={project} service={service} buildId={b.id} />
                    </div>
                  )}
                </li>
              );
            });
          })()}
        </ul>
      )}
    </div>
  );
}

// BuildLogs streams the build pod's logs. LogStream is keyed on env
// today; we encode the build id as env=build:<id> so the server can
// route to the kaniko pod by name. If the server doesn't recognise
// it we fall through to "no logs available" (the server side handles
// that case gracefully).
function BuildLogs({ project, service, buildId }: { project: string; service: string; buildId: string }) {
  return (
    <div className="h-72 p-2">
      <LogStream
        project={project}
        service={service}
        env={`build:${buildId}`}
        height="100%"
      />
    </div>
  );
}
