"use client";

// Runs tab in ServiceOverlay. Lists recent KusoRuns + an inline
// composer to fire a new run (command + optional env-var overlay +
// timeout). Phase pills mirror builds: pending/running/succeeded/
// failed/cancelled with the same colour family so the tabs read
// consistently.

import { useState } from "react";
import { useCan, Perms } from "@/features/auth";
import {
  useRuns,
  useCreateRun,
  useCancelRun,
  runPhase,
  runMessage,
  runCompletedAt,
  type KusoRun,
} from "@/features/services";
import { relativeTime } from "@/lib/format";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { LogStream } from "@/components/logs/LogStream";
import { Play, X, Terminal, AlertCircle, ChevronDown, ChevronRight } from "lucide-react";
import { toast } from "sonner";
import { cn } from "@/lib/utils";

interface Props {
  project: string;
  service: string;
}

type RunPhase = "pending" | "running" | "succeeded" | "failed" | "cancelled" | "unknown";

function classify(phase: string): RunPhase {
  switch (phase) {
    case "pending":
    case "running":
    case "succeeded":
    case "failed":
    case "cancelled":
      return phase;
    default:
      return "unknown";
  }
}

function phaseBadge(p: RunPhase) {
  const map: Record<RunPhase, { label: string; cls: string }> = {
    pending:   { label: "PENDING",   cls: "bg-[var(--bg-tertiary)] text-[var(--text-secondary)] border-[var(--border-subtle)]" },
    running:   { label: "RUNNING",   cls: "bg-[var(--building-subtle)] text-[var(--building)] border-[var(--building)]/30" },
    succeeded: { label: "SUCCEEDED", cls: "bg-emerald-500/10 text-emerald-400 border-emerald-500/30" },
    failed:    { label: "FAILED",    cls: "bg-red-500/10 text-red-400 border-red-500/30" },
    cancelled: { label: "CANCELLED", cls: "bg-[var(--bg-tertiary)] text-[var(--text-tertiary)] border-[var(--border-subtle)]" },
    unknown:   { label: "UNKNOWN",   cls: "bg-[var(--bg-tertiary)] text-[var(--text-tertiary)] border-[var(--border-subtle)]" },
  };
  const m = map[p];
  return (
    <span
      className={cn(
        "inline-flex shrink-0 items-center rounded px-1.5 py-0.5 font-mono text-[9px] font-semibold tracking-widest border",
        m.cls,
      )}
    >
      {m.label}
    </span>
  );
}

// triggerLabel mirrors ServiceDeploymentsPanel's labelling.
function triggerLabel(r: KusoRun): string {
  const src = r.spec.triggeredBy ?? "";
  const user = r.spec.triggeredByUser ?? "";
  if (src === "user") return user ? `by ${user}` : "by you";
  if (src === "api") return "via API";
  if (src === "system") return "via system";
  return "";
}

export function ServiceRunsPanel({ project, service }: Props) {
  const runs = useRuns(project, service);
  const canRun = useCan(Perms.ServicesWrite);
  const [openRun, setOpenRun] = useState<string | null>(null);

  return (
    <div className="space-y-4">
      {canRun ? (
        <NewRunComposer project={project} service={service} />
      ) : (
        <p className="rounded-md border border-dashed border-[var(--border-subtle)] p-3 font-mono text-[11px] text-[var(--text-tertiary)]">
          services:write required to fire runs. Read-only listing below.
        </p>
      )}

      {runs.isPending ? (
        <div className="space-y-2">
          <Skeleton className="h-14 w-full" />
          <Skeleton className="h-14 w-full" />
        </div>
      ) : (runs.data ?? []).length === 0 ? (
        <p className="rounded-md border border-dashed border-[var(--border-subtle)] p-6 text-center text-sm text-[var(--text-tertiary)]">
          No runs yet. Fire one above to execute a migration, seed, or one-off
          script against this service&apos;s most-recent built image.
        </p>
      ) : (
        <ul className="space-y-2">
          {(runs.data ?? []).map((r) => (
            <RunRow
              key={r.metadata.name}
              project={project}
              service={service}
              run={r}
              canCancel={canRun}
              isOpen={openRun === r.metadata.name}
              onToggle={() =>
                setOpenRun((cur) => (cur === r.metadata.name ? null : r.metadata.name))
              }
            />
          ))}
        </ul>
      )}
    </div>
  );
}

// NewRunComposer — single textarea for command + KEY=VAL env list +
// timeout slider. Deliberately spartan; this is an admin/operator
// surface, not a dev tool.
function NewRunComposer({ project, service }: { project: string; service: string }) {
  const [cmd, setCmd] = useState("");
  const [envText, setEnvText] = useState("");
  const [timeoutSec, setTimeoutSec] = useState(1800);
  const create = useCreateRun(project, service);

  const onFire = async () => {
    const parts = parseCommand(cmd);
    if (parts.length === 0) {
      toast.error("Command is empty");
      return;
    }
    const envOverlay = parseEnv(envText);
    if (envOverlay === null) {
      toast.error("Env vars must be KEY=VALUE, one per line");
      return;
    }
    try {
      await create.mutateAsync({
        command: parts,
        env: envOverlay.length > 0 ? envOverlay : undefined,
        timeoutSeconds: timeoutSec || undefined,
      });
      toast.success("Run started");
      setCmd("");
      setEnvText("");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to start run");
    }
  };

  return (
    <div className="space-y-3 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-4">
      <div className="flex items-center gap-2 text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
        <Terminal className="h-3.5 w-3.5" />
        Fire a one-shot run
      </div>
      <div>
        <label className="font-mono text-[10px] text-[var(--text-tertiary)]">
          command
        </label>
        <textarea
          value={cmd}
          onChange={(e) => setCmd(e.target.value)}
          placeholder={`python manage.py migrate\n\n# or, with shell:\nsh -c "rake db:seed && echo done"`}
          rows={3}
          className="mt-1 w-full resize-y rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-3 py-2 font-mono text-[12px] text-[var(--text-primary)] focus:border-[var(--border-strong)] focus:outline-none"
          spellCheck={false}
        />
        <p className="mt-1 font-mono text-[10px] text-[var(--text-tertiary)]">
          parsed as argv (space-separated). Use <code>sh -c &quot;…&quot;</code>{" "}
          when you need shell expansion.
        </p>
      </div>
      <div>
        <label className="font-mono text-[10px] text-[var(--text-tertiary)]">
          env overlay (optional; one KEY=VALUE per line)
        </label>
        <textarea
          value={envText}
          onChange={(e) => setEnvText(e.target.value)}
          placeholder="MIGRATE_VERSION=20260518"
          rows={2}
          className="mt-1 w-full resize-y rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-3 py-2 font-mono text-[12px] text-[var(--text-primary)] focus:border-[var(--border-strong)] focus:outline-none"
          spellCheck={false}
        />
      </div>
      <div className="flex items-center justify-between gap-3">
        <label className="flex items-center gap-2 font-mono text-[10px] text-[var(--text-tertiary)]">
          timeout
          <input
            type="number"
            value={timeoutSec}
            onChange={(e) => setTimeoutSec(Number(e.target.value) || 1800)}
            min={1}
            max={86400}
            className="w-20 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 py-1 font-mono text-[11px]"
          />
          seconds
        </label>
        <Button size="sm" onClick={onFire} disabled={create.isPending || !cmd.trim()}>
          <Play className="h-3.5 w-3.5" />
          {create.isPending ? "Starting…" : "Fire run"}
        </Button>
      </div>
    </div>
  );
}

// parseCommand splits a multi-line command box on whitespace,
// honouring (basic) double-quoted segments so `sh -c "cmd a b"`
// works without forcing the user to escape spaces. The server still
// gets argv as JSON; this is the client-side tokenizer.
function parseCommand(s: string): string[] {
  const out: string[] = [];
  let buf = "";
  let inQuote = false;
  for (let i = 0; i < s.length; i++) {
    const c = s[i];
    if (c === '"') {
      inQuote = !inQuote;
      continue;
    }
    if (!inQuote && /\s/.test(c)) {
      if (buf) {
        out.push(buf);
        buf = "";
      }
      continue;
    }
    buf += c;
  }
  if (buf) out.push(buf);
  return out;
}

// parseEnv parses the "KEY=VAL" textarea. Empty lines + lines
// starting with `#` are ignored. Returns null on a malformed line so
// the caller can surface a clear error.
function parseEnv(s: string): { name: string; value: string }[] | null {
  const out: { name: string; value: string }[] = [];
  for (const raw of s.split("\n")) {
    const line = raw.trim();
    if (!line || line.startsWith("#")) continue;
    const i = line.indexOf("=");
    if (i <= 0) return null;
    out.push({ name: line.slice(0, i), value: line.slice(i + 1) });
  }
  return out;
}

function RunRow({
  project,
  service,
  run,
  canCancel,
  isOpen,
  onToggle,
}: {
  project: string;
  service: string;
  run: KusoRun;
  canCancel: boolean;
  isOpen: boolean;
  onToggle: () => void;
}) {
  const phase = classify(runPhase(run));
  const name = run.metadata.name;
  const created = run.metadata.creationTimestamp;
  const cmd = (run.spec.command ?? []).join(" ");
  const msg = runMessage(run);
  const completed = runCompletedAt(run);
  const inflight = phase === "pending" || phase === "running";
  return (
    <li
      className={cn(
        "overflow-hidden rounded-md border bg-[var(--bg-secondary)]",
        phase === "failed" && "border-red-500/30",
        phase === "succeeded" && "border-emerald-500/30",
        phase === "running" && "border-amber-500/30",
        !["failed", "succeeded", "running"].includes(phase) && "border-[var(--border-subtle)]",
      )}
    >
      <div className="flex items-center gap-1 px-3 py-2.5">
        <button
          type="button"
          onClick={onToggle}
          className="flex flex-1 items-center gap-3 text-left"
        >
          {phaseBadge(phase)}
          <div className="min-w-0 flex-1">
            <div className="truncate font-mono text-[12px] text-[var(--text-primary)]" title={cmd}>
              {cmd || <span className="text-[var(--text-tertiary)]">(no command)</span>}
            </div>
            <div className="mt-0.5 font-mono text-[10px] text-[var(--text-tertiary)]">
              {created ? relativeTime(created) : "—"}
              {completed && phase !== "running" && phase !== "pending" && (
                <>
                  {" · finished "}
                  {relativeTime(completed)}
                </>
              )}
              {triggerLabel(run) && (
                <>
                  {" · "}
                  <span>{triggerLabel(run)}</span>
                </>
              )}
              {(run.spec.env?.length ?? 0) > 0 && (
                <>
                  {" · "}
                  <span title={(run.spec.env ?? []).map((e) => `${e.name}=${e.value}`).join(" ")}>
                    {run.spec.env!.length} env override{run.spec.env!.length === 1 ? "" : "s"}
                  </span>
                </>
              )}
            </div>
            {phase === "failed" && msg && (
              <div className="mt-1 flex items-start gap-1 font-mono text-[11px] text-red-300/90" title={msg}>
                <AlertCircle className="mt-[1px] h-3 w-3 shrink-0" />
                <span className="truncate">{msg}</span>
              </div>
            )}
          </div>
        </button>
        {inflight && canCancel && (
          <CancelRunButton project={project} service={service} runName={name} />
        )}
        <button
          type="button"
          onClick={onToggle}
          className="rounded p-1 text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
          title={isOpen ? "Hide logs" : "Show logs"}
        >
          {isOpen ? (
            <ChevronDown className="h-4 w-4 shrink-0" />
          ) : (
            <ChevronRight className="h-4 w-4 shrink-0" />
          )}
        </button>
      </div>
      {isOpen && (
        <div className="min-w-0 border-t border-[var(--border-subtle)] bg-[var(--bg-primary)]">
          <div className="h-[360px]">
            <LogStream
              project={project}
              service={service}
              env={`run:${name}`}
              height="100%"
            />
          </div>
        </div>
      )}
    </li>
  );
}

function CancelRunButton({
  project,
  service,
  runName,
}: {
  project: string;
  service: string;
  runName: string;
}) {
  const m = useCancelRun(project, service);
  return (
    <button
      type="button"
      onClick={() => {
        m.mutate(runName, {
          onSuccess: () => toast.success("Run cancelled"),
          onError: (e) =>
            toast.error(e instanceof Error ? e.message : "Cancel failed"),
        });
      }}
      disabled={m.isPending}
      title="Cancel this run"
      className="inline-flex items-center gap-1 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 py-1 font-mono text-[10px] text-[var(--text-secondary)] hover:border-red-500/40 hover:bg-red-500/5 hover:text-red-400 disabled:opacity-50"
    >
      <X className="h-3 w-3" />
      {m.isPending ? "…" : "cancel"}
    </button>
  );
}
