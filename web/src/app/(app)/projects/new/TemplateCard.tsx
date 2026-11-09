"use client";

import Link from "next/link";
import { ChevronRight, Store } from "lucide-react";
import { useMarketplace, type MarketplaceApp } from "@/features/marketplace";
import { AppIcon } from "@/components/marketplace/AppIcon";

// Well-known apps lead the strip; the rest of the catalog fills in.
const FEATURED = ["gitea", "n8n", "metabase", "plausible", "uptime-kuma", "vaultwarden"];

export function featuredApps(apps: readonly MarketplaceApp[], n: number): MarketplaceApp[] {
  const byName = new Map(apps.map((a) => [a.name, a]));
  const lead = FEATURED.map((f) => byName.get(f)).filter((a): a is MarketplaceApp => !!a);
  const rest = apps.filter((a) => !FEATURED.includes(a.name));
  return [...lead, ...rest].slice(0, n);
}

// TemplateCard is the "start from a template" option: a few app logos
// and the catalog size instead of a list of names.
export function TemplateCard() {
  const market = useMarketplace();
  const apps = market.data ?? [];
  const shown = featuredApps(apps, 5);
  const titles = shown.map((a) => a.title).join(", ");
  return (
    <Link
      href="/marketplace"
      aria-label={apps.length > 0 ? `Templates: ${apps.length} one-click apps, including ${titles}` : "Templates"}
      className="group flex h-full flex-col gap-1.5 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-3 py-3 text-left transition-colors hover:border-[var(--border-strong)] hover:bg-[var(--bg-tertiary)]"
    >
      <span className="flex items-center justify-between">
        {shown.length > 0 ? (
          <span className="flex -space-x-1" title={titles}>
            {shown.map((a) => (
              <AppIcon
                key={a.name}
                app={a.name}
                title={a.title}
                className="h-6 w-6 rounded-md ring-2 ring-[var(--bg-secondary)] group-hover:ring-[var(--bg-tertiary)]"
              />
            ))}
          </span>
        ) : (
          <Store className="h-4 w-4 text-[var(--accent)]" aria-hidden />
        )}
        <ChevronRight
          aria-hidden
          className="h-4 w-4 text-[var(--text-tertiary)] transition-transform motion-safe:group-hover:translate-x-0.5"
        />
      </span>
      <span className="text-[13px] font-medium text-[var(--text-primary)]">Template</span>
      <span className="text-xs leading-snug text-[var(--text-secondary)]">
        {apps.length > 0 ? `${apps.length} one-click apps` : market.isPending ? "One-click apps" : "One-click apps from the marketplace"}
      </span>
    </Link>
  );
}
