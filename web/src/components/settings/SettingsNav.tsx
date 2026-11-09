"use client";

import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { useEffect, useMemo, useState } from "react";
import { ChevronDown, ChevronRight, ChevronsUpDown, Lock, Search, X } from "lucide-react";
import { useSession } from "@/features/auth";
import { useSystemVersion } from "@/features/system-version";
import { Sheet, SheetContent, SheetTitle } from "@/components/ui/sheet";
import { cn } from "@/lib/utils";
import {
  SETTINGS_GROUPS,
  SETTINGS_NAV,
  activeSettingsHref,
  scoreSettingsItem,
  type SettingsNavItem,
} from "./nav";

export interface SettingsAccess {
  // allowed is undefined while the session loads; callers treat that as
  // "show everything" so the nav doesn't flash empty.
  allowed: (item: SettingsNavItem) => boolean;
  ready: boolean;
}

export function useSettingsAccess(): SettingsAccess {
  const { data, isPending } = useSession();
  const perms = data?.session.permissions;
  return useMemo(
    () => ({
      ready: !isPending,
      allowed: (item: SettingsNavItem) => {
        if (!item.perm) return true;
        if (isPending) return true;
        return (perms ?? []).includes(item.perm);
      },
    }),
    [perms, isPending],
  );
}

// useIsDesktop mirrors Tailwind's lg breakpoint. Used only to decide
// which of two search boxes owns data-primary-search, so "/" focuses
// the one the user can actually see.
export function useIsDesktop(): boolean {
  const [desktop, setDesktop] = useState(false);
  useEffect(() => {
    const mq = window.matchMedia("(min-width: 1024px)");
    const on = () => setDesktop(mq.matches);
    on();
    mq.addEventListener("change", on);
    return () => mq.removeEventListener("change", on);
  }, []);
  return desktop;
}

interface FilteredGroup {
  id: string;
  label: string;
  items: SettingsNavItem[];
  lockedCount: number;
}

export function useFilteredSettingsNav(query: string): {
  groups: FilteredGroup[];
  flat: SettingsNavItem[];
} {
  const access = useSettingsAccess();
  return useMemo(() => {
    const q = query.trim();
    const visible = SETTINGS_NAV.filter((it) => access.allowed(it));
    if (q) {
      const flat = visible
        .map((it) => ({ it, s: scoreSettingsItem(it, q) }))
        .filter((x) => x.s > 0)
        .sort((a, b) => b.s - a.s)
        .map((x) => x.it);
      return { groups: [], flat };
    }
    const groups = SETTINGS_GROUPS.map((g) => {
      const all = SETTINGS_NAV.filter((it) => it.group === g.id);
      const items = all.filter((it) => access.allowed(it));
      return { id: g.id, label: g.label, items, lockedCount: all.length - items.length };
    }).filter((g) => g.items.length > 0 || g.lockedCount > 0);
    return { groups, flat: groups.flatMap((g) => g.items) };
  }, [query, access]);
}

// SettingsNavList is the grouped, filterable list shared by the desktop
// sidebar and the mobile sheet.
function SettingsNavList({
  primarySearch,
  autoFocus,
  onNavigate,
}: {
  primarySearch: boolean;
  autoFocus?: boolean;
  onNavigate?: () => void;
}) {
  const pathname = usePathname();
  const router = useRouter();
  const [query, setQuery] = useState("");
  const { groups, flat } = useFilteredSettingsNav(query);
  const active = activeSettingsHref(pathname ?? "");
  const searching = query.trim().length > 0;
  const badges = useSettingsBadges();
  // Only the current page's group starts open (all of them on the
  // landing page). Manual toggles are remembered until the person moves
  // to another group, which re-opens that group.
  const activeGroup = SETTINGS_NAV.find((it) => it.href === active)?.group ?? null;
  const [overrides, setOverrides] = useState<{ ctx: string | null; map: Record<string, boolean> }>({
    ctx: activeGroup,
    map: {},
  });
  const map = overrides.ctx === activeGroup ? overrides.map : {};
  const isOpen = (id: string) => map[id] ?? (activeGroup === null || id === activeGroup);
  const toggle = (id: string) => setOverrides({ ctx: activeGroup, map: { ...map, [id]: !isOpen(id) } });
  // Long groups list their everyday pages first; the rest are one click.
  const [showAll, setShowAll] = useState<Record<string, boolean>>({});

  return (
    <nav aria-label="Settings" className="flex flex-col gap-4">
      <div className="relative">
        <Search className="pointer-events-none absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-[var(--text-tertiary)]" />
        <input
          type="search"
          value={query}
          autoFocus={autoFocus}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && flat[0]) {
              router.push(flat[0].href);
              setQuery("");
              onNavigate?.();
            } else if (e.key === "Escape" && query) {
              e.stopPropagation();
              setQuery("");
            }
          }}
          placeholder="Find a setting"
          aria-label="Find a setting"
          {...(primarySearch ? { "data-primary-search": "" } : {})}
          className="h-8 w-full rounded-sm border border-[var(--border-subtle)] bg-[var(--bg-secondary)] pl-8 pr-7 text-sm text-[var(--text-primary)] outline-none placeholder:text-[var(--text-tertiary)] focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 [&::-webkit-search-cancel-button]:hidden"
        />
        {searching && (
          <button
            type="button"
            onClick={() => setQuery("")}
            aria-label="Clear search"
            className="absolute right-1.5 top-1/2 -translate-y-1/2 rounded p-0.5 text-[var(--text-tertiary)] hover:text-[var(--text-primary)]"
          >
            <X className="h-3.5 w-3.5" />
          </button>
        )}
      </div>

      {searching ? (
        flat.length === 0 ? (
          <p className="px-2 text-sm text-[var(--text-tertiary)]">No settings match &ldquo;{query.trim()}&rdquo;.</p>
        ) : (
          <ul className="space-y-0.5">
            {flat.map((it) => (
              <li key={it.href}>
                <NavLink item={it} active={it.href === active} onNavigate={onNavigate} showDescription />
              </li>
            ))}
          </ul>
        )
      ) : (
        groups.map((g) => {
          const open = isOpen(g.id);
          const badged = !open && g.items.some((it) => badges[it.href]);
          return (
            <div key={g.id}>
              <button
                type="button"
                onClick={() => toggle(g.id)}
                aria-expanded={open}
                aria-controls={`settings-group-${g.id}`}
                className="mb-1 flex w-full items-center gap-1 rounded-sm px-2 py-0.5 text-left text-xs font-medium text-[var(--text-tertiary)] hover:text-[var(--text-primary)]"
              >
                <ChevronDown
                  aria-hidden
                  className={cn("h-3.5 w-3.5 shrink-0 transition-transform", !open && "-rotate-90")}
                />
                <span className="flex-1">{g.label}</span>
                {badged && (
                  <span className="size-1.5 rounded-full bg-[var(--info)]">
                    <span className="sr-only">(has updates)</span>
                  </span>
                )}
              </button>
              {open && (
                <div id={`settings-group-${g.id}`}>
                  <ul className="space-y-0.5">
                    {visibleGroupItems(g.items, !!showAll[g.id], active, badges).map((it) => (
                      <li key={it.href}>
                        <NavLink item={it} active={it.href === active} onNavigate={onNavigate} badge={badges[it.href]} />
                      </li>
                    ))}
                  </ul>
                  {visibleGroupItems(g.items, !!showAll[g.id], active, badges).length < g.items.length && (
                    <button
                      type="button"
                      onClick={() => setShowAll((m) => ({ ...m, [g.id]: true }))}
                      className="mt-0.5 px-2 py-1 text-xs text-[var(--text-secondary)] hover:text-[var(--text-primary)]"
                    >
                      Show {g.items.length - visibleGroupItems(g.items, false, active, badges).length} more
                    </button>
                  )}
                  {g.lockedCount > 0 && (
                    <div
                      className="mt-1 flex items-center gap-1.5 px-2 text-xs text-[var(--text-tertiary)]"
                      title="Ask an instance admin for access"
                    >
                      <Lock className="h-3 w-3" />
                      {g.lockedCount} more need admin access
                    </div>
                  )}
                </div>
              )}
            </div>
          );
        })
      )}
    </nav>
  );
}

// visibleGroupItems trims a long group to its primary pages, keeping the
// active page and anything with a badge so nothing current is hidden.
function visibleGroupItems(
  items: SettingsNavItem[],
  all: boolean,
  active: string | null,
  badges: Record<string, string | undefined>,
): SettingsNavItem[] {
  if (all || items.length <= 6 || !items.some((it) => it.primary)) return items;
  return items.filter((it) => it.primary || it.href === active || badges[it.href]);
}

// useSettingsBadges marks nav items that have something waiting, kept
// quiet on purpose: the bell is the primary surface for an update.
function useSettingsBadges(): Record<string, string | undefined> {
  const version = useSystemVersion();
  return version.data?.needsUpdate ? { "/settings/updates": `${version.data.latest} available` } : {};
}

function NavLink({
  item,
  active,
  onNavigate,
  showDescription = false,
  badge,
}: {
  item: SettingsNavItem;
  active: boolean;
  onNavigate?: () => void;
  showDescription?: boolean;
  badge?: string;
}) {
  const Icon = item.icon;
  return (
    <Link
      href={item.href}
      onClick={onNavigate}
      aria-current={active ? "page" : undefined}
      className={cn(
        "flex items-start gap-2.5 rounded-sm px-2 py-1.5 text-sm transition-colors",
        active
          ? "bg-[var(--bg-tertiary)] font-medium text-[var(--text-primary)]"
          : "text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)]/60 hover:text-[var(--text-primary)]",
      )}
    >
      <Icon className={cn("mt-0.5 h-4 w-4 shrink-0", active ? "text-[var(--text-primary)]" : "text-[var(--text-tertiary)]")} />
      <span className="min-w-0 flex-1">
        <span className="block truncate">{item.title}</span>
        {showDescription && (
          <span className="block text-xs font-normal text-[var(--text-tertiary)]">{item.description}</span>
        )}
      </span>
      {badge && (
        <span title={badge} className="mt-1.5 size-1.5 shrink-0 rounded-full bg-[var(--info)]">
          <span className="sr-only">{badge}</span>
        </span>
      )}
    </Link>
  );
}

export function SettingsSidebar() {
  const desktop = useIsDesktop();
  return <SettingsNavList primarySearch={desktop} />;
}

// SettingsMobileSwitcher is the compact section switcher shown below lg:
// the current section as a button that opens the full nav in a sheet.
export function SettingsMobileSwitcher() {
  const pathname = usePathname();
  const [open, setOpen] = useState(false);
  const active = SETTINGS_NAV.find((it) => it.href === activeSettingsHref(pathname ?? ""));
  if (!active) return null;
  const Icon = active.icon;
  return (
    <div className="flex items-center gap-1 border-b border-[var(--border-subtle)] py-2 lg:hidden">
      <Link
        href="/settings"
        className="shrink-0 rounded-sm px-1.5 py-1 text-sm text-[var(--text-secondary)] hover:text-[var(--text-primary)]"
      >
        Settings
      </Link>
      <ChevronRight className="h-3.5 w-3.5 shrink-0 text-[var(--text-tertiary)]" />
      <button
        type="button"
        onClick={() => setOpen(true)}
        className="flex min-w-0 items-center gap-2 rounded-sm px-2 py-1 text-sm font-medium text-[var(--text-primary)] hover:bg-[var(--bg-tertiary)]"
        aria-haspopup="dialog"
      >
        <Icon className="h-4 w-4 shrink-0 text-[var(--text-tertiary)]" />
        <span className="truncate">{active.title}</span>
        <ChevronsUpDown className="h-3.5 w-3.5 shrink-0 text-[var(--text-tertiary)]" />
      </button>
      <Sheet open={open} onOpenChange={setOpen}>
        <SheetContent side="left" className="w-[85vw] max-w-sm overflow-y-auto p-4">
          <SheetTitle className="mb-1">Settings</SheetTitle>
          <SettingsNavList primarySearch={false} onNavigate={() => setOpen(false)} />
        </SheetContent>
      </Sheet>
    </div>
  );
}

// SettingsOverviewList is the settings root: every reachable section
// grouped, with descriptions. On mobile it doubles as the navigation.
export function SettingsOverviewList() {
  const desktop = useIsDesktop();
  const [query, setQuery] = useState("");
  const { groups, flat } = useFilteredSettingsNav(query);
  const searching = query.trim().length > 0;
  return (
    <div className="space-y-8">
      <div className="relative lg:hidden">
        <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-[var(--text-tertiary)]" />
        <input
          type="search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Find a setting"
          aria-label="Find a setting"
          {...(!desktop ? { "data-primary-search": "" } : {})}
          className="h-10 w-full rounded-sm border border-[var(--border-subtle)] bg-[var(--bg-secondary)] pl-9 pr-3 text-sm text-[var(--text-primary)] outline-none placeholder:text-[var(--text-tertiary)] focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50"
        />
      </div>
      {searching ? (
        <OverviewGrid items={flat} empty={`No settings match “${query.trim()}”.`} />
      ) : (
        groups.map((g) => {
          const meta = SETTINGS_GROUPS.find((x) => x.id === g.id);
          return (
            <section key={g.id}>
              <div className="mb-3 flex items-baseline justify-between gap-3">
                <h2 className="text-sm font-semibold text-[var(--text-primary)]">{g.label}</h2>
                <span className="text-xs text-[var(--text-tertiary)]">{meta?.hint}</span>
              </div>
              <OverviewGrid items={g.items} />
              {g.lockedCount > 0 && (
                <p className="mt-2 flex items-center gap-1.5 text-xs text-[var(--text-tertiary)]">
                  <Lock className="h-3 w-3" />
                  {g.lockedCount} more {g.lockedCount === 1 ? "section needs" : "sections need"} admin access. Ask an
                  instance admin if you need them.
                </p>
              )}
            </section>
          );
        })
      )}
    </div>
  );
}

function OverviewGrid({ items, empty }: { items: SettingsNavItem[]; empty?: string }) {
  if (items.length === 0) {
    return empty ? <p className="text-sm text-[var(--text-tertiary)]">{empty}</p> : null;
  }
  return (
    <ul className="divide-y divide-[var(--border-subtle)] overflow-hidden rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] sm:grid sm:grid-cols-2 sm:divide-y-0 sm:gap-px sm:bg-[var(--border-subtle)] sm:[&>li:last-child:nth-child(odd)]:col-span-2">
      {items.map((it) => {
        const Icon = it.icon;
        return (
          <li key={it.href} className="bg-[var(--bg-secondary)]">
            <Link
              href={it.href}
              className="group flex h-full items-start gap-3 px-4 py-3.5 transition-colors hover:bg-[var(--bg-tertiary)]/50"
            >
              <span className="mt-0.5 inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-md bg-[var(--bg-tertiary)] text-[var(--text-tertiary)] group-hover:text-[var(--text-primary)]">
                <Icon className="h-4 w-4" />
              </span>
              <span className="min-w-0 flex-1">
                <span className="block text-sm font-medium text-[var(--text-primary)]">{it.title}</span>
                <span className="mt-0.5 block text-xs leading-relaxed text-[var(--text-secondary)]">
                  {it.description}
                </span>
              </span>
              <ChevronRight className="mt-2 h-4 w-4 shrink-0 text-[var(--text-tertiary)] opacity-0 transition-opacity group-hover:opacity-100" />
            </Link>
          </li>
        );
      })}
    </ul>
  );
}
