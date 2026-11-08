"use client";

import { smoothScroll } from "@/lib/reduced-motion";
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { ChevronDown, ChevronLeft, ChevronRight } from "lucide-react";
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
}

const BADGE_TONE = {
  error: "bg-[var(--error-subtle)] text-[var(--error)]",
  warning: "bg-[var(--warning-subtle)] text-[var(--warning)]",
  neutral: "bg-[var(--bg-tertiary)] text-[var(--text-secondary)]",
} as const;

const MORE_WIDTH = 84;
// Below this the strip scrolls sideways instead of folding into "More":
// on a phone a swipe is cheaper than a popover.
const SCROLL_QUERY = "(max-width: 639px)";

// OverlayTabs is an ARIA tablist for the service/addon overlays. On desktop
// tabs that don't fit move into a "More" popover (the active tab always
// stays in the strip); on phones the strip scrolls horizontally with a fade
// hint instead. `pinned` sits at the right edge at every width. Arrow keys,
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
  const measureRef = useRef<HTMLDivElement>(null);
  const [fitCount, setFitCount] = useState(tabs.length);
  const [moreOpen, setMoreOpen] = useState(false);
  const [scrollMode, setScrollMode] = useState(false);
  const [fade, setFade] = useState({ left: false, right: false });

  const updateFade = useCallback(() => {
    const strip = stripRef.current;
    if (!strip) return;
    const max = strip.scrollWidth - strip.clientWidth;
    setFade({ left: strip.scrollLeft > 2, right: max - strip.scrollLeft > 2 });
  }, []);

  const scrollByPage = (dir: 1 | -1) => {
    const strip = stripRef.current;
    if (!strip) return;
    strip.scrollBy({ left: dir * strip.clientWidth * 0.7, behavior: smoothScroll() });
  };

  const measure = useCallback(() => {
    const strip = stripRef.current;
    const m = measureRef.current;
    if (!strip || !m) return;
    if (window.matchMedia(SCROLL_QUERY).matches) {
      setScrollMode(true);
      setFitCount(tabs.length);
      updateFade();
      return;
    }
    setScrollMode(false);
    setFade({ left: false, right: false });
    const avail = strip.clientWidth;
    const widths = Array.from(m.children).map((c) => (c as HTMLElement).offsetWidth);
    const total = widths.reduce((a, b) => a + b, 0);
    if (total <= avail) {
      setFitCount(tabs.length);
      return;
    }
    let used = MORE_WIDTH;
    let n = 0;
    for (const w of widths) {
      if (used + w > avail) break;
      used += w;
      n++;
    }
    setFitCount(Math.max(1, n));
  }, [tabs.length, updateFade]);

  useLayoutEffect(() => {
    measure();
    const strip = stripRef.current;
    if (!strip || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(measure);
    ro.observe(strip);
    // Badges and web fonts change tab widths without resizing the strip.
    Array.from(strip.children).forEach((c) => ro.observe(c));
    return () => ro.disconnect();
  }, [measure, tabs]);

  // Phones: keep the active tab scrolled into view (deep links to Shell,
  // g-chords, Back/Forward).
  useEffect(() => {
    if (!scrollMode) return;
    const strip = stripRef.current;
    const el = strip?.querySelector<HTMLElement>(`#${CSS.escape(`${idPrefix}-tab-${active}`)}`);
    if (!strip || !el) return;
    const left = el.offsetLeft - strip.offsetLeft;
    // 48px = the edge fade; a tab under it doesn't count as visible.
    const lo = strip.scrollLeft > 0 ? strip.scrollLeft + 48 : 0;
    if (left < lo || left + el.offsetWidth > strip.scrollLeft + strip.clientWidth - 48) {
      strip.scrollTo({ left: Math.max(0, left - 48), behavior: smoothScroll() });
    }
  }, [scrollMode, active, idPrefix]);

  // The active tab must stay visible: if it would overflow, it takes the
  // last visible slot.
  const { visible, overflow } = useMemo(() => {
    if (fitCount >= tabs.length) return { visible: tabs, overflow: [] as OverlayTabItem<T>[] };
    const head = tabs.slice(0, fitCount);
    if (!head.some((t) => t.id === active)) {
      const act = tabs.find((t) => t.id === active);
      if (act) head.splice(head.length - 1, 1, act);
    }
    const ids = new Set(head.map((t) => t.id));
    return { visible: tabs.filter((t) => ids.has(t.id)), overflow: tabs.filter((t) => !ids.has(t.id)) };
  }, [tabs, fitCount, active]);

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
        {/* Edge fades say "more this way"; the chevron scrolls a page.
            Pointer-only: keyboard users arrow through the tabs. */}
        {scrollMode && fade.left && (
          <button
            type="button"
            tabIndex={-1}
            aria-hidden
            onClick={() => scrollByPage(-1)}
            className="absolute inset-y-0 left-0 z-10 flex w-12 items-center justify-start bg-gradient-to-r from-[var(--bg-primary)] from-40% to-transparent text-[var(--text-tertiary)]"
          >
            <ChevronLeft className="h-4 w-4" />
          </button>
        )}
        {scrollMode && fade.right && (
          <button
            type="button"
            tabIndex={-1}
            aria-hidden
            onClick={() => scrollByPage(1)}
            className="absolute inset-y-0 right-0 z-10 flex w-12 items-center justify-end bg-gradient-to-l from-[var(--bg-primary)] from-40% to-transparent text-[var(--text-tertiary)]"
          >
            <ChevronRight className="h-4 w-4" />
          </button>
        )}
        <div
          ref={stripRef}
          onScroll={scrollMode ? updateFade : undefined}
          className={cn(
            "relative flex min-w-0 flex-1 items-center",
            scrollMode
              ? "overflow-x-auto overscroll-x-contain [scrollbar-width:none] [&::-webkit-scrollbar]:hidden"
              : "overflow-hidden",
          )}
        >
          {visible.map((t) => tabButton(t))}
          {overflow.length > 0 && (
            <Popover open={moreOpen} onOpenChange={setMoreOpen}>
              <PopoverTrigger
                data-more-trigger=""
                tabIndex={-1}
                aria-label={`More tabs: ${overflow.map((t) => t.label).join(", ")}`}
                className="relative inline-flex h-10 shrink-0 items-center gap-1 px-3 text-sm font-medium text-[var(--text-secondary)] outline-none hover:text-[var(--text-primary)] focus-visible:text-[var(--text-primary)] focus-visible:underline"
              >
                More
                {overflowBadge && <span aria-hidden className="h-1.5 w-1.5 rounded-full bg-[var(--error)]" />}
                <ChevronDown aria-hidden className="h-3.5 w-3.5" />
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
        </div>
      </div>
      {pinned && tabButton(pinned, true)}
      {/* Off-screen copy of every tab at natural width, for measuring. */}
      <div ref={measureRef} aria-hidden className="pointer-events-none invisible absolute left-0 top-0 flex h-0 overflow-hidden whitespace-nowrap">
        {tabs.map((t) => (
          <span key={t.id} className="inline-flex shrink-0 items-center gap-1.5 px-3 text-sm font-medium">
            {t.icon && <span className="inline-block h-3.5 w-3.5" />}
            {t.label}
            {!!t.badge && <span className="px-1.5 text-xs font-semibold">{t.badge > 99 ? "99+" : t.badge}</span>}
          </span>
        ))}
      </div>
    </div>
  );
}
