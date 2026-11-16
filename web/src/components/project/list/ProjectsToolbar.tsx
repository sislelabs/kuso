"use client";

import { Select } from "@/components/ui/select";
import { Search, X } from "lucide-react";
import { cn } from "@/lib/utils";
import type { StatusFilter } from "./projectCardModel";
import type { ProjectSort } from "./useListPrefs";

const FILTERS: ReadonlyArray<{
  id: StatusFilter;
  label: string;
  dot?: string;
  always?: boolean;
  hint?: string;
}> = [
  { id: "all", label: "All", always: true },
  { id: "live", label: "Live", dot: "bg-[var(--success)]", always: true },
  {
    id: "failing",
    label: "Failing",
    dot: "bg-[var(--error)]",
    always: true,
    // Failing is rollout health only; log errors are their own chip.
    hint: "A service is crashing, degraded, or its build or release failed. Errors in the logs are counted separately on each card.",
  },
  { id: "building", label: "Deploying", dot: "bg-[var(--building)]" },
  { id: "sleeping", label: "Sleeping", dot: "bg-[var(--info)]" },
  {
    id: "stopped",
    label: "Stopped",
    dot: "bg-[var(--text-tertiary)]",
    always: true,
  },
];

export function ProjectsToolbar({
  query,
  onQuery,
  status,
  onStatus,
  counts,
  sort,
  onSort,
}: {
  query: string;
  onQuery: (q: string) => void;
  status: StatusFilter;
  onStatus: (s: StatusFilter) => void;
  counts: Record<StatusFilter, number> | null;
  sort: ProjectSort;
  onSort: (s: ProjectSort) => void;
}) {
  // Deploying / Sleeping only appear when something is in that state —
  // or when the remembered filter points at them.
  const visible = FILTERS.filter(
    (f) => f.always || f.id === status || (counts?.[f.id] ?? 0) > 0,
  );
  return (
    <div className="mb-6 flex flex-wrap items-center gap-x-3 gap-y-3 lg:flex-nowrap">
      <div
        role="group"
        aria-label="Filter by status"
        className="order-last -mx-1 flex w-full min-w-0 items-center overflow-x-auto px-1 py-0.5 [scrollbar-width:none] lg:order-none lg:w-auto lg:flex-1"
      >
        <div className="flex items-center gap-0.5 rounded-[10px] border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-[3px]">
          {visible.map((f) => {
            const active = status === f.id;
            const n = counts?.[f.id];
            const empty = !active && n === 0;
            return (
              <button
                key={f.id}
                type="button"
                aria-pressed={active}
                title={f.hint}
                onClick={() => onStatus(f.id)}
                className={cn(
                  "inline-flex h-[30px] shrink-0 items-center gap-1.5 rounded-[7px] px-3 text-[13px] font-medium outline-none transition-colors focus-visible:ring-2 focus-visible:ring-[var(--ring)]",
                  active
                    ? "bg-[var(--bg-elevated)] text-[var(--text-primary)] shadow-[inset_0_1px_0_rgb(255_255_255/0.06),0_1px_3px_rgb(0_0_0/0.4)]"
                    : empty
                      ? "text-[var(--text-placeholder)] hover:text-[var(--text-secondary)]"
                      : "text-[var(--text-secondary)] hover:text-[var(--text-primary)]",
                )}
              >
                {f.dot && <span aria-hidden className={cn("size-1.5 rounded-full", f.dot, empty && "opacity-50")} />}
                {f.label}
                {n !== undefined && (
                  <span
                    className={cn(
                      "tabular-nums",
                      active ? "text-[var(--text-secondary)]" : empty ? "text-[var(--text-placeholder)]" : "text-[var(--text-tertiary)]",
                    )}
                  >
                    {n}
                  </span>
                )}
              </button>
            );
          })}
        </div>
      </div>

      <div className="relative min-w-0 flex-1 lg:w-72 lg:flex-none">
        <Search
          aria-hidden
          className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-[var(--text-tertiary)]"
        />
        <input
          type="search"
          data-primary-search
          value={query}
          onChange={(e) => onQuery(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Escape" && query) {
              e.preventDefault();
              onQuery("");
            }
          }}
          placeholder="Filter by project, service or domain"
          aria-label="Search projects"
          className="h-9 w-full rounded-[10px] border border-[var(--border-subtle)] bg-[var(--bg-secondary)] pl-8 pr-8 text-[13px] text-[var(--text-primary)] outline-none placeholder:text-[var(--text-tertiary)] hover:border-[var(--border-subtle)] focus-visible:border-[var(--ring)] focus-visible:ring-2 focus-visible:ring-[var(--ring)]/30 [&::-webkit-search-cancel-button]:hidden"
        />
        {query ? (
          <button
            type="button"
            onClick={() => onQuery("")}
            aria-label="Clear search"
            className="absolute right-1 top-1/2 inline-flex h-6 w-6 -translate-y-1/2 items-center justify-center rounded text-[var(--text-tertiary)] hover:text-[var(--text-primary)]"
          >
            <X className="h-3.5 w-3.5" />
          </button>
        ) : (
          <kbd
            aria-hidden
            className="pointer-events-none absolute right-2 top-1/2 hidden -translate-y-1/2 rounded border border-[var(--border-default)] px-1.5 font-mono text-[10.5px] leading-4 text-[var(--text-placeholder)] md:block"
          >
            /
          </kbd>
        )}
      </div>

      <label className="flex shrink-0 items-center gap-2 text-xs text-[var(--text-tertiary)]">
        <span className="sr-only md:not-sr-only">Sort</span>
        <Select
          size="sm"
          value={sort}
          onChange={(e) => onSort(e.target.value === "deployed" ? "deployed" : "name")}
          wrapperClassName="max-w-[9.5rem]"
        >
          <option value="name">Name</option>
          <option value="deployed">Recently deployed</option>
        </Select>
      </label>
    </div>
  );
}
