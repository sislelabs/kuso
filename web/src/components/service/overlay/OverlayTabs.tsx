"use client";

import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { ChevronDown } from "lucide-react";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { cn } from "@/lib/utils";

export interface OverlayTabItem<T extends string> {
  id: T;
  label: string;
  icon?: React.ComponentType<{ className?: string }>;
  // Small count shown after the label (errors in the last 24h, …).
  badge?: number;
  badgeTone?: "error" | "warning" | "neutral";
  // Second key of the "g" chord that jumps here, shown in the tooltip.
  chord?: string;
  // Always listed under "More" (unless active), whatever the width, so
  // the strip reads the same for every service.
  secondary?: boolean;
}

const BADGE_TONE = {
  error: "bg-[var(--error-subtle)] text-[var(--error)]",
  warning: "bg-[var(--warning-subtle)] text-[var(--warning)]",
  neutral: "bg-[var(--bg-tertiary)] text-[var(--text-secondary)]",
} as const;

// OverlayTabs is an ARIA tablist for the service/addon overlays. The order
// never changes with width: primary tabs sit in a strip that scrolls
// sideways (with fade edges) when it doesn't fit, and `secondary` tabs
// always live under "More". `pinned` sits at the right edge. Arrow keys,
// Home and End move focus; Enter/Space activates (manual activation, so
// arrowing past Logs doesn't open a log stream on the way).
export function OverlayTabs<T extends string>({
  tabs,
  pinned,
  active,
  onChange,
  idPrefix,
  label,
}: {
  tabs: OverlayTabItem<T>[];
  pinned?: OverlayTabItem<T>;
  active: T;
  onChange: (id: T) => void;
  idPrefix: string;
  label: string;
}) {
  const stripRef = useRef<HTMLDivElement>(null);
  const [moreOpen, setMoreOpen] = useState(false);
  const [fade, setFade] = useState({ left: false, right: false });
  const visible = useMemo(() => tabs.filter((t) => !t.secondary), [tabs]);
  const overflow = useMemo(() => tabs.filter((t) => t.secondary), [tabs]);
  const activeInMore = overflow.find((t) => t.id === active);

  const updateFade = useCallback(() => {
    const el = stripRef.current;
    if (!el) return;
    setFade({ left: el.scrollLeft > 2, right: el.scrollLeft + el.clientWidth < el.scrollWidth - 2 });
  }, []);

  useLayoutEffect(() => {
    updateFade();
    const el = stripRef.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(updateFade);
    ro.observe(el);
    return () => ro.disconnect();
  }, [updateFade, visible]);

  // Bring the active tab into view without moving the page.
  useEffect(() => {
    const el = stripRef.current;
    const tab = el?.querySelector<HTMLElement>(`#${CSS.escape(`${idPrefix}-tab-${active}`)}`);
    if (!el || !tab) return;
    const left = tab.offsetLeft - el.offsetLeft;
    if (left < el.scrollLeft) el.scrollLeft = left - 8;
    else if (left + tab.offsetWidth > el.scrollLeft + el.clientWidth) el.scrollLeft = left + tab.offsetWidth - el.clientWidth + 8;
    updateFade();
  }, [active, idPrefix, updateFade]);

  const onKeyDown = (e: React.KeyboardEvent<HTMLDivElement>) => {
    const keys = ["ArrowLeft", "ArrowRight", "Home", "End"];
    if (!keys.includes(e.key)) return;
    const root = e.currentTarget;
    const els = Array.from(root.querySelectorAll<HTMLElement>('[role="tab"], [data-more-trigger]'));
    const idx = els.indexOf(document.activeElement as HTMLElement);
    if (idx < 0) return;
    e.preventDefault();
    let next = idx;
    if (e.key === "ArrowRight") next = (idx + 1) % els.length;
    if (e.key === "ArrowLeft") next = (idx - 1 + els.length) % els.length;
    if (e.key === "Home") next = 0;
    if (e.key === "End") next = els.length - 1;
    els[next]?.focus();
  };

  const tabButton = (t: OverlayTabItem<T>, isPinned = false) => {
    const selected = t.id === active;
    const Icon = t.icon;
    return (
      <button
        key={t.id}
        type="button"
        role="tab"
        id={`${idPrefix}-tab-${t.id}`}
        aria-selected={selected}
        aria-controls={`${idPrefix}-panel`}
        tabIndex={selected ? 0 : -1}
        title={t.chord ? `${t.label} (G then ${t.chord.toUpperCase()})` : t.label}
        onClick={() => onChange(t.id)}
        className={cn(
          "relative inline-flex h-10 shrink-0 items-center gap-1.5 whitespace-nowrap px-3 text-sm font-medium outline-none transition-colors",
          "focus-visible:after:absolute focus-visible:after:inset-x-1 focus-visible:after:inset-y-1.5 focus-visible:after:rounded-md focus-visible:after:ring-2 focus-visible:after:ring-[var(--ring)]",
          selected ? "text-[var(--text-primary)]" : "text-[var(--text-secondary)] hover:text-[var(--text-primary)]",
          isPinned && "border-l border-[var(--border-subtle)] pl-3.5",
        )}
      >
        {Icon && <Icon aria-hidden className="h-3.5 w-3.5" />}
        {t.label}
        {!!t.badge && (
          <span className={cn("rounded-full px-1.5 text-xs font-semibold tabular-nums leading-[18px]", BADGE_TONE[t.badgeTone ?? "neutral"])}>
            {t.badge > 99 ? "99+" : t.badge}
            <span className="sr-only"> {t.badgeTone === "error" ? "errors" : "items"}</span>
          </span>
        )}
        {selected && <span aria-hidden className="absolute inset-x-3 bottom-0 h-[2px] rounded-full bg-[var(--text-primary)]" />}
      </button>
    );
  };

  const overflowBadge = overflow.some((t) => !!t.badge && t.badgeTone === "error");

  return (
    <div
      role="tablist"
      aria-label={label}
      onKeyDown={onKeyDown}
      className="relative flex shrink-0 items-center border-b border-[var(--border-subtle)] px-2 sm:px-3"
    >
      <div className="relative flex min-w-0 flex-1">
        <div
          ref={stripRef}
          onScroll={updateFade}
          className="relative flex min-w-0 flex-1 items-center overflow-x-auto overscroll-x-contain [scrollbar-width:none] [&::-webkit-scrollbar]:hidden"
        >
          {visible.map((t) => tabButton(t))}
        </div>
        {fade.left && (
          <span aria-hidden className="pointer-events-none absolute inset-y-0 left-0 w-6 bg-gradient-to-r from-[var(--bg-primary)] to-transparent" />
        )}
        {fade.right && (
          <span aria-hidden className="pointer-events-none absolute inset-y-0 right-0 w-6 bg-gradient-to-l from-[var(--bg-primary)] to-transparent" />
        )}
      </div>
      {overflow.length > 0 && (
            <Popover open={moreOpen} onOpenChange={setMoreOpen}>
              <PopoverTrigger
                data-more-trigger=""
                tabIndex={-1}
                aria-label={`More tabs: ${overflow.map((t) => t.label).join(", ")}${activeInMore ? ` (showing ${activeInMore.label})` : ""}`}
                className={cn(
                  "relative inline-flex h-10 shrink-0 items-center gap-1 whitespace-nowrap px-3 text-sm font-medium outline-none hover:text-[var(--text-primary)] focus-visible:text-[var(--text-primary)] focus-visible:underline",
                  activeInMore ? "text-[var(--text-primary)]" : "text-[var(--text-secondary)]",
                )}
              >
                {activeInMore ? activeInMore.label : "More"}
                {overflowBadge && <span aria-hidden className="h-1.5 w-1.5 rounded-full bg-[var(--error)]" />}
                <ChevronDown aria-hidden className="h-3.5 w-3.5" />
                {activeInMore && <span aria-hidden className="absolute inset-x-3 bottom-0 h-[2px] rounded-full bg-[var(--text-primary)]" />}
              </PopoverTrigger>
              <PopoverContent align="start" className="w-52 gap-0.5 rounded-lg p-1">
                {overflow.map((t) => {
                  const Icon = t.icon;
                  return (
                    <button
                      key={t.id}
                      type="button"
                      onClick={() => {
                        setMoreOpen(false);
                        onChange(t.id);
                      }}
                      className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
                    >
                      {Icon && <Icon aria-hidden className="h-3.5 w-3.5" />}
                      <span className="flex-1">{t.label}</span>
                      {!!t.badge && (
                        <span className={cn("rounded-full px-1.5 text-xs font-semibold tabular-nums", BADGE_TONE[t.badgeTone ?? "neutral"])}>
                          {t.badge}
                        </span>
                      )}
                      {t.chord && (
                        <kbd className="font-sans text-xs text-[var(--text-tertiary)]">G {t.chord.toUpperCase()}</kbd>
                      )}
                    </button>
                  );
                })}
              </PopoverContent>
            </Popover>
      )}
      {pinned && tabButton(pinned, true)}
    </div>
  );
}
