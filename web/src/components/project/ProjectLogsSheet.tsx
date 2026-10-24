"use client";

import { useMemo, useState } from "react";
import { Search, X } from "lucide-react";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { QueryErrorState } from "@/components/shared/QueryErrorState";
import { LogList } from "@/components/service/overlay/ServiceLogsPanel";
import { useEnvironments } from "@/features/projects";
import { useProjectLogSearch } from "@/features/project-overview";
import { serviceEnvOptions } from "@/lib/env-group";
import { cn } from "@/lib/utils";

const RANGES = ["10m", "1h", "6h", "24h", "7d"] as const;
type Range = (typeof RANGES)[number];

function sinceToISO(range: Range): string {
  const n = parseInt(range, 10);
  const unit = range.slice(-1);
  const ms = unit === "d" ? n * 86_400_000 : unit === "h" ? n * 3_600_000 : n * 60_000;
  return new Date(Date.now() - ms).toISOString();
}

// ProjectLogsSheet searches the log archive across every service in the
// project (GET /api/projects/{p}/logs/search) — "what crashed in this
// project in the last hour" without opening each service.
export function ProjectLogsSheet({
  project,
  open,
  onOpenChange,
  defaultEnv,
}: {
  project: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  defaultEnv: string;
}) {
  const [q, setQ] = useState("");
  const [env, setEnv] = useState(defaultEnv);
  const [committed, setCommitted] = useState<{ q: string; env: string; since: Range; sinceISO: string }>(() => ({
    q: "",
    env: defaultEnv,
    since: "1h",
    sinceISO: sinceToISO("1h"),
  }));

  const envs = useEnvironments(project);
  const envOptions = useMemo(() => {
    const all = envs.data ?? [];
    const seen = new Map<string, string>();
    for (const fqn of new Set(all.map((e) => e.spec.service ?? ""))) {
      for (const o of serviceEnvOptions(all, fqn)) seen.set(o.value, o.label);
    }
    return [...seen.entries()].map(([value, label]) => ({ value, label }));
  }, [envs.data]);

  const search = useProjectLogSearch(project, committed, open);
  const lines = useMemo(() => (search.data?.pages ?? []).flatMap((p) => p.lines ?? []), [search.data]);

  const commit = (patch: Partial<{ q: string; env: string; since: Range }>) =>
    setCommitted((c) => {
      const next = { ...c, ...patch };
      return { ...next, sinceISO: sinceToISO(next.since) };
    });

  const prefix = project + "-";
  const serviceLabel = (l: { service?: string }) =>
    l.service?.startsWith(prefix) ? l.service.slice(prefix.length) : (l.service ?? "");

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="data-[side=right]:w-full data-[side=right]:sm:max-w-3xl">
        <SheetHeader>
          <SheetTitle className="font-mono text-sm">Logs · {project}</SheetTitle>
          <SheetDescription className="font-mono text-[11px]">
            Archive search across every service in the project. Substring match, case-insensitive.
          </SheetDescription>
        </SheetHeader>
        <div className="flex min-h-0 flex-1 flex-col gap-3 px-4 pb-4">
          <form
            onSubmit={(e) => {
              e.preventDefault();
              commit({ q, env });
            }}
            className="flex flex-wrap items-center gap-2"
          >
            <div className="relative min-w-[200px] flex-1">
              <Search className="pointer-events-none absolute left-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-[var(--text-tertiary)]" />
              <Input
                value={q}
                onChange={(e) => setQ(e.target.value)}
                aria-label="Search log text"
                placeholder="Search log text — panic"
                className="h-8 pl-7 font-mono text-[12px]"
              />
              {q && (
                <button
                  type="button"
                  onClick={() => {
                    setQ("");
                    commit({ q: "" });
                  }}
                  aria-label="Clear search"
                  className="absolute right-1.5 top-1/2 -translate-y-1/2 rounded p-0.5 text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
                >
                  <X className="h-3 w-3" />
                </button>
              )}
            </div>
            <select
              value={env}
              onChange={(e) => {
                setEnv(e.target.value);
                commit({ env: e.target.value });
              }}
              aria-label="Environment"
              className="h-8 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 font-mono text-[11px]"
            >
              <option value="">all envs</option>
              {envOptions.map((o) => (
                <option key={o.value} value={o.value}>
                  {o.label}
                </option>
              ))}
            </select>
            <div className="inline-flex rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-0.5">
              {RANGES.map((r) => (
                <button
                  key={r}
                  type="button"
                  onClick={() => commit({ since: r })}
                  className={cn(
                    "rounded px-2 py-1 font-mono text-[10px] transition-colors",
                    committed.since === r
                      ? "bg-[var(--bg-tertiary)] text-[var(--text-primary)]"
                      : "text-[var(--text-tertiary)] hover:text-[var(--text-primary)]",
                  )}
                >
                  {r}
                </button>
              ))}
            </div>
            <Button type="submit" size="sm">
              Search
            </Button>
          </form>

          {search.isPending ? (
            <Skeleton className="h-64 w-full" />
          ) : search.isError ? (
            <QueryErrorState what="project logs" error={search.error} onRetry={() => void search.refetch()} />
          ) : lines.length === 0 ? (
            <p className="rounded-md border border-dashed border-[var(--border-subtle)] px-4 py-8 text-center text-[12px] text-[var(--text-tertiary)]">
              {committed.q
                ? `No matches for ${committed.q} in the last ${committed.since}.`
                : `No log lines from this project in the last ${committed.since}.`}
            </p>
          ) : (
            <LogList
              lines={lines}
              highlight={committed.q}
              serviceLabel={serviceLabel}
              hasMore={!!search.hasNextPage}
              loadingMore={search.isFetchingNextPage}
              onLoadMore={() => {
                if (search.hasNextPage && !search.isFetchingNextPage) void search.fetchNextPage();
              }}
            />
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}
