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

// TemplateCard is the "start from a template" option: three named apps
// and how many more the catalog has.
export function TemplateCard() {
  const market = useMarketplace();
  const apps = market.data ?? [];
  const shown = featuredApps(apps, 3);
  const titles = shown.map((a) => a.title).join(", ");
  const more = apps.length - shown.length;
  return (
    <Link
      href="/marketplace"
      aria-label={apps.length > 0 ? `Templates: ${apps.length} one-click apps, including ${titles}` : "Templates"}
      className="group flex h-full flex-col gap-1.5 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-3 py-3 text-left transition-colors hover:border-[var(--border-strong)] hover:bg-[var(--bg-tertiary)]"
    >
      <span className="flex items-center justify-between">
        <Store className="h-4 w-4 text-[var(--accent)]" aria-hidden />
        <ChevronRight
          aria-hidden
          className="h-4 w-4 text-[var(--text-tertiary)] transition-transform motion-safe:group-hover:translate-x-0.5"
        />
      </span>
      <span className="text-[13px] font-medium text-[var(--text-primary)]">Template</span>
      {shown.length > 0 ? (
        <span className="flex flex-col gap-1">
          {shown.map((a) => (
            <span key={a.name} className="flex min-w-0 items-center gap-1.5 text-xs text-[var(--text-secondary)]">
              <AppIcon app={a.name} title={a.title} className="h-4 w-4 rounded" />
              <span className="truncate">{a.title}</span>
            </span>
          ))}
          {more > 0 && <span className="text-xs text-[var(--text-tertiary)]">+{more} more one-click apps</span>}
        </span>
      ) : (
        <span className="text-xs leading-snug text-[var(--text-secondary)]">
          {market.isPending ? "One-click apps" : "One-click apps from the marketplace"}
        </span>
      )}
    </Link>
  );
}
