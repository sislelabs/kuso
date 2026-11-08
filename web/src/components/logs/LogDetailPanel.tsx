"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { Check, Copy, ListTree, X } from "lucide-react";
import { fetchLogContext } from "@/features/logs";
import type { LogScope } from "@/features/logs";
import { cn } from "@/lib/utils";
import { claimEscape } from "@/lib/escape-layer";
import { Button } from "@/components/ui/button";
import { AnsiText } from "./AnsiText";
import { LogLineText } from "./LogLineText";
import { fmtClock, parseJsonLine, stripAnsi } from "./logParse";
import type { LogLevel } from "./logParse";

interface DetailLine {
  key: string;
  id?: number;
  ts: string;
  pod: string;
  line: string;
  stream?: string;
  service?: string;
  env?: string;
  approx?: boolean;
  backlog?: boolean;
}

const LEVEL_PILL: Record<LogLevel, string> = {
  error: "border-[var(--error)]/40 bg-[var(--error-subtle)] text-[var(--error)]",
  warn: "border-[var(--warning)]/40 bg-[var(--warning-subtle)] text-[var(--warning)]",
  info: "border-[var(--info)]/40 bg-[var(--info-subtle)] text-[var(--info)]",
  debug: "border-[var(--border-default)] text-[var(--text-secondary)]",
  none: "border-[var(--border-default)] text-[var(--text-tertiary)]",
};

// LogDetailPanel is the drawer a clicked log line opens: full message,
// metadata, parsed JSON fields and the surrounding lines from the archive.
export function LogDetailPanel({
  line,
  level,
  scope,
  env,
  serviceLabel,
  onClose,
}: {
  line: DetailLine;
  level: LogLevel;
  scope: LogScope;
  env?: string;
  serviceLabel?: (fqn: string) => string;
  onClose: () => void;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const [showContext, setShowContext] = useState(false);
  const [copied, setCopied] = useState(false);
  const json = useMemo(() => parseJsonLine(line.line), [line.line]);

  useEffect(() => {
    setShowContext(false);
    setCopied(false);
  }, [line.key]);

  useEffect(() => {
    ref.current?.focus();
  }, []);

  // Escape closes this panel, not the overlay around it: capture phase +
  // claimEscape, and data-escape-layer marks us as the top surface.
  const closeRef = useRef(onClose);
  closeRef.current = onClose;
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || e.defaultPrevented) return;
      claimEscape(e);
      closeRef.current();
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, []);

  const ctx = useQuery({
    queryKey: ["log-context", scope.project, scope.service ?? "*", env ?? "", line.id ?? (line.ts || line.key), line.pod],
    // A replayed line has no time; it was written before the stream
    // connected, so search back from now and find it by pod + text.
    queryFn: () =>
      fetchLogContext(scope, env, { id: line.id, ts: line.ts || new Date().toISOString(), pod: line.pod, line: line.line }),
    enabled: showContext,
    staleTime: 60_000,
  });

  const anchorRef = useRef<HTMLDivElement>(null);
  const ctxBoxRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    // Scroll only the context box; scrollIntoView would also scroll the
    // overlay behind it.
    const box = ctxBoxRef.current;
    const a = anchorRef.current;
    if (box && a) box.scrollTop = a.offsetTop - box.clientHeight / 2 + a.clientHeight / 2;
  }, [ctx.data]);

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(stripAnsi(line.line));
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      toast.error("Copy failed — clipboard unavailable");
    }
  };

  const d = new Date(line.ts);
  const svc = line.service ? (serviceLabel ? serviceLabel(line.service) : line.service) : undefined;
  const meta: [string, string | undefined][] = [
    line.backlog
      ? ["Time", "Unknown — written before you connected; this kuso server doesn't send log timestamps"]
      : line.approx
      ? ["Received", `${fmtClock(line.ts, true)} (local) — arrival time; this kuso server doesn't send log timestamps`]
      : ["Time", Number.isNaN(d.getTime()) ? line.ts : `${fmtClock(line.ts, true)} (local)`],
    ["UTC", Number.isNaN(d.getTime()) || line.approx ? undefined : d.toISOString()],
    ["Service", svc],
    ["Replica", line.pod || undefined],
    ["Environment", line.env || env],
    ["Stream", line.stream],
  ];

  return (
    <div
      ref={ref}
      tabIndex={-1}
      role="dialog"
      aria-label="Log line details"
      data-escape-layer
      className="absolute inset-y-0 right-0 z-20 flex w-full flex-col border-l border-[var(--border-default)] bg-[var(--bg-elevated)] shadow-[var(--shadow-lg)] outline-none sm:w-[min(30rem,92%)]"
    >
      <header className="flex items-center gap-2 border-b border-[var(--border-subtle)] px-4 py-2.5">
        <span className={cn("rounded border px-1.5 py-0.5 text-xs font-semibold capitalize", LEVEL_PILL[level])}>
          {level === "none" ? "log" : level}
        </span>
        <span className={cn("min-w-0 flex-1 truncate text-xs text-[var(--text-secondary)]", !line.backlog && "font-mono")}>
          {line.backlog ? "Earlier output · time unknown" : line.approx ? `~${fmtClock(line.ts, true).slice(0, -4)}` : fmtClock(line.ts, true)}
        </span>
        <Button size="sm" variant="outline" onClick={() => void copy()}>
          {copied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
          {copied ? "Copied" : "Copy line"}
        </Button>
        <button
          type="button"
          onClick={onClose}
          aria-label="Close details (Esc)"
          className="rounded p-1 text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
        >
          <X className="h-4 w-4" />
        </button>
      </header>

      <div className="min-h-0 flex-1 overflow-y-auto">
        <section className="px-4 py-3">
          <h4 className="mb-1.5 text-xs font-medium text-[var(--text-secondary)]">Message</h4>
          <pre className="max-h-64 overflow-auto whitespace-pre-wrap rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-3 font-mono text-xs leading-relaxed text-[var(--text-primary)] [overflow-wrap:anywhere]">
            <AnsiText text={line.line} />
          </pre>
        </section>

        <section className="px-4 pb-3">
          <dl className="grid grid-cols-[6.5rem_1fr] gap-x-3 gap-y-1.5 text-xs">
            {meta
              .filter(([, v]) => !!v)
              .map(([k, v]) => (
                <div key={k} className="contents">
                  <dt className="text-[var(--text-tertiary)]">{k}</dt>
                  <dd className="min-w-0 break-all font-mono text-[var(--text-primary)]">{v}</dd>
                </div>
              ))}
          </dl>
        </section>

        {json && (
          <section className="border-t border-[var(--border-subtle)] px-4 py-3">
            <h4 className="mb-1.5 text-xs font-medium text-[var(--text-secondary)]">Fields</h4>
            <dl className="grid grid-cols-[minmax(6rem,auto)_1fr] gap-x-3 gap-y-1 text-xs">
              {Object.entries(json).map(([k, v]) => (
                <div key={k} className="contents">
                  <dt className="truncate font-mono text-[var(--text-tertiary)]" title={k}>
                    {k}
                  </dt>
                  <dd className="min-w-0 break-all font-mono text-[var(--text-primary)]">
                    {typeof v === "string" ? v : JSON.stringify(v)}
                  </dd>
                </div>
              ))}
            </dl>
          </section>
        )}

        <section className="border-t border-[var(--border-subtle)] px-4 py-3">
          {!showContext ? (
            <Button
              size="sm"
              variant="outline"
              onClick={() => {
                setShowContext(true);
                // The button unmounts; keep focus inside the panel.
                ref.current?.focus();
              }}
            >
              <ListTree className="h-3.5 w-3.5" /> View in context
            </Button>
          ) : (
            <>
              <h4 className="mb-1.5 text-xs font-medium text-[var(--text-secondary)]">
                Surrounding lines <span className="font-normal text-[var(--text-tertiary)]">· ±50, unfiltered, from the archive</span>
              </h4>
              {ctx.isPending ? (
                <p className="text-xs text-[var(--text-tertiary)]">Loading context…</p>
              ) : ctx.isError ? (
                <p className="text-xs text-[var(--error)]">
                  Couldn&apos;t load context.{" "}
                  <button type="button" className="underline" onClick={() => void ctx.refetch()}>
                    Retry
                  </button>
                </p>
              ) : ctx.data.lines.length === 0 ? (
                <p className="text-xs text-[var(--text-tertiary)]">
                  This line isn&apos;t in the archive yet — lines are shipped every few seconds. Try again shortly.
                </p>
              ) : (
                <div ref={ctxBoxRef} className="relative max-h-[22rem] overflow-auto rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] py-1 font-mono text-xs leading-relaxed">
                  {ctx.data.lines.map((l, i) => {
                    const isAnchor = i === ctx.data.anchorIndex;
                    return (
                      <div
                        key={l.id}
                        ref={isAnchor ? anchorRef : undefined}
                        className={cn(
                          "flex gap-2 px-2",
                          isAnchor && "bg-[var(--bg-tertiary)] ring-1 ring-inset ring-[var(--border-default)]",
                        )}
                      >
                        <span className="shrink-0 tabular-nums text-[var(--text-tertiary)]">{fmtClock(l.ts, false).slice(0, 8)}</span>
                        <span className="min-w-0 whitespace-pre-wrap text-[var(--text-primary)] [overflow-wrap:anywhere]">
                          <LogLineText line={l.line} />
                        </span>
                      </div>
                    );
                  })}
                </div>
              )}
            </>
          )}
        </section>
      </div>
    </div>
  );
}
