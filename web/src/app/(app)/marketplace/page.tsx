"use client";

import { PageHeader } from "@/components/shared/PageHeader";
import { Suspense, useMemo, useState } from "react";
import { useSearchParams } from "next/navigation";
import Link from "next/link";
import { ArrowRight, Search, SearchX, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { QueryErrorState } from "@/components/shared/QueryErrorState";
import {
  markdownToPlain,
  parseInlineMarkdown,
  truncateMarkdown,
  useMarketplace,
  versionDisplay,
  type MarketplaceApp,
} from "@/features/marketplace";
import { DeployDialog } from "@/components/marketplace/DeployDialog";
import { AppIcon, categoryLabel } from "@/components/marketplace/AppIcon";
import { renderMarkdown } from "@/components/marketplace/InlineMarkdown";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/utils";
import { Perms, useCan } from "@/features/auth";

// Below this many templates the catalog reads as a starter set, so admins
// get a pointer to where new ones come from.
const SMALL_CATALOG = 12;

// useSearchParams needs a Suspense boundary in the static export.
export default function MarketplacePage() {
  return (
    <Suspense>
      <Marketplace />
    </Suspense>
  );
}

function Marketplace() {
  // ?project=<name> (linked from an empty project) preselects the target.
  const targetProject = useSearchParams().get("project")?.trim() || undefined;
  const market = useMarketplace();
  const isAdmin = useCan(Perms.SettingsAdmin);
  const apps = useMemo(() => market.data ?? [], [market.data]);
  const [q, setQ] = useState("");
  const [cat, setCat] = useState<string>("all");
  const [selected, setSelected] = useState<MarketplaceApp | null>(null);

  const categories = useMemo(() => {
    const counts = new Map<string, number>();
    for (const a of apps) counts.set(a.category, (counts.get(a.category) ?? 0) + 1);
    return Array.from(counts.entries()).sort(([a], [b]) => a.localeCompare(b));
  }, [apps]);

  const needle = q.trim().toLowerCase();
  const filtered = apps.filter(
    (a) =>
      (cat === "all" || a.category === cat) &&
      (needle === "" || `${a.title} ${a.name} ${a.description} ${a.category}`.toLowerCase().includes(needle)),
  );

  return (
    <div className="mx-auto max-w-6xl px-4 py-6 sm:px-6 lg:p-8">
      <PageHeader
        className="mb-6 sm:mb-6"
        breadcrumb={
          targetProject
            ? [
                { label: "Projects", href: "/projects" },
                { label: targetProject, href: `/projects/${encodeURIComponent(targetProject)}` },
                { label: "Marketplace" },
              ]
            : [{ label: "Projects", href: "/projects" }, { label: "Marketplace" }]
        }
        title="Marketplace"
        description={
          targetProject ? (
            <>
              Self-hosted apps, pre-configured for kuso. Pick one to deploy it into{" "}
              <span className="font-mono text-[var(--text-primary)]">{targetProject}</span> with storage and secrets set up.
            </>
          ) : (
            "Self-hosted apps, pre-configured for kuso. Pick one, choose a project, and it deploys with storage and secrets set up."
          )
        }
      />

      <div className="mb-5 flex flex-col gap-3 md:flex-row md:items-center">
        <div className="relative w-full md:w-80 md:flex-none">
          <Search
            aria-hidden
            className="pointer-events-none absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-[var(--text-tertiary)]"
          />
          <input
            type="search"
            data-primary-search
            value={q}
            onChange={(e) => setQ(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape" && q) {
                e.preventDefault();
                setQ("");
              }
            }}
            placeholder="Search apps"
            aria-label="Search apps"
            className="h-9 w-full rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] pl-8 pr-8 text-sm text-[var(--text-primary)] outline-none placeholder:text-[var(--text-tertiary)] focus-visible:border-[var(--ring)] focus-visible:ring-2 focus-visible:ring-[var(--ring)]/30 [&::-webkit-search-cancel-button]:hidden"
          />
          {q && (
            <button
              type="button"
              onClick={() => setQ("")}
              aria-label="Clear search"
              className="absolute right-1.5 top-1/2 inline-flex h-6 w-6 -translate-y-1/2 items-center justify-center rounded text-[var(--text-tertiary)] hover:text-[var(--text-primary)]"
            >
              <X className="h-3.5 w-3.5" />
            </button>
          )}
        </div>
        <div
          role="group"
          aria-label="Filter by category"
          className="-mx-1 flex items-center gap-1.5 overflow-x-auto px-1 py-0.5 [scrollbar-width:none] md:flex-wrap"
        >
          {[["all", apps.length] as const, ...categories].map(([c, n]) => {
            const active = cat === c;
            return (
              <button
                key={c}
                type="button"
                aria-pressed={active}
                onClick={() => setCat(c)}
                className={cn(
                  "inline-flex h-8 shrink-0 items-center gap-1.5 rounded-full border px-3 text-xs font-medium outline-none transition-colors focus-visible:ring-2 focus-visible:ring-[var(--ring)]",
                  active
                    ? "border-transparent bg-[var(--text-primary)] text-[var(--bg-primary)]"
                    : "border-[var(--border-subtle)] text-[var(--text-secondary)] hover:text-[var(--text-primary)]",
                )}
              >
                {c === "all" ? "All" : categoryLabel(c)}
                <span className={cn("tabular-nums", active ? "opacity-70" : "text-[var(--text-tertiary)]")}>{n}</span>
              </button>
            );
          })}
        </div>
      </div>

      {market.isPending ? (
        <ul className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3" aria-busy="true" aria-label="Loading apps">
          {[0, 1, 2, 3, 4, 5].map((i) => (
            <li key={i} className="flex gap-3 rounded-lg border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-4">
              <Skeleton className="h-10 w-10 shrink-0 rounded-lg" />
              <div className="flex-1">
                <Skeleton className="h-4 w-28 rounded" />
                <Skeleton className="mt-2 h-3 w-full rounded" />
                <Skeleton className="mt-3 h-4 w-16 rounded-full" />
              </div>
            </li>
          ))}
        </ul>
      ) : market.isError ? (
        <QueryErrorState what="the marketplace" error={market.error} onRetry={() => void market.refetch()} />
      ) : filtered.length === 0 ? (
        <div className="flex flex-col items-center rounded-lg border border-dashed border-[var(--border-subtle)] px-6 py-14 text-center">
          <SearchX className="h-6 w-6 text-[var(--text-tertiary)]" aria-hidden />
          <p className="mt-3 text-sm font-medium text-[var(--text-primary)]">No apps match</p>
          <p className="mt-1 text-[13px] text-[var(--text-secondary)]">Try another search or category.</p>
          <Button
            variant="secondary"
            size="sm"
            className="mt-4"
            onClick={() => {
              setQ("");
              setCat("all");
            }}
          >
            Clear filters
          </Button>
        </div>
      ) : (
        <ul className="grid auto-rows-fr grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {filtered.map((a) => (
            <li key={a.name}>
              <AppCard app={a} onSelect={() => setSelected(a)} />
            </li>
          ))}
        </ul>
      )}

      {isAdmin && market.isSuccess && apps.length > 0 && apps.length < SMALL_CATALOG && (
        <p className="mt-6 text-center text-[13px] text-[var(--text-secondary)]">
          More apps arrive with kuso updates.{" "}
          <Link href="/settings/updates" className="text-[var(--link)] underline underline-offset-4 hover:text-[var(--text-primary)]">
            Check for updates
          </Link>
        </p>
      )}

      {selected && <DeployDialog app={selected} defaultProject={targetProject} onClose={() => setSelected(null)} />}
    </div>
  );
}

// Roughly two lines of card text; line-clamp stays as a narrow-screen backstop.
const CARD_DESCRIPTION_CHARS = 78;

function AppCard({ app: a, onSelect }: { app: MarketplaceApp; onSelect: () => void }) {
  const description = useMemo(() => {
    const nodes = parseInlineMarkdown(a.description);
    return { full: markdownToPlain(nodes).replace(/\s+/g, " ").trim(), ...truncateMarkdown(nodes, CARD_DESCRIPTION_CHARS) };
  }, [a.description]);
  const version = versionDisplay(a.appVersion);
  return (
    <button
      type="button"
      data-app-card
      onClick={onSelect}
      aria-label={`Deploy ${a.title}`}
      className="group flex h-full w-full items-start gap-3 rounded-lg border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-4 text-left outline-none transition-colors hover:border-[var(--border-strong)] hover:bg-[var(--bg-tertiary)]/40 focus-visible:border-[var(--ring)] focus-visible:ring-2 focus-visible:ring-[var(--ring)]/40"
    >
      <AppIcon app={a.name} title={a.title} className="h-10 w-10" />
      <span className="flex min-w-0 flex-1 flex-col">
        <span className="flex items-baseline justify-between gap-2">
          <span className="truncate text-[15px] font-semibold text-[var(--text-primary)]">{a.title}</span>
          <span className="inline-flex shrink-0 items-center gap-1 self-center text-xs font-medium text-[var(--text-tertiary)] opacity-0 transition-opacity group-hover:opacity-100 group-focus-visible:opacity-100">
            Deploy
            <ArrowRight aria-hidden className="h-3.5 w-3.5 motion-safe:transition-transform motion-safe:group-hover:translate-x-0.5" />
          </span>
        </span>
        <span
          className="mt-1 line-clamp-2 text-[13px] leading-snug text-[var(--text-secondary)]"
          title={description.truncated ? description.full : undefined}
        >
          {renderMarkdown(description.nodes, { links: false })}
          {description.truncated && "…"}
        </span>
        <span className="mt-auto flex items-center gap-2 pt-3 text-xs text-[var(--text-tertiary)]">
          <span className="rounded-full border border-[var(--border-subtle)] px-2 py-0.5 text-[var(--text-secondary)]">
            {categoryLabel(a.category)}
          </span>
          {version && (
            <span className={cn(version.literal && "font-mono")} title={version.detail}>
              {version.label}
            </span>
          )}
        </span>
      </span>
    </button>
  );
}
