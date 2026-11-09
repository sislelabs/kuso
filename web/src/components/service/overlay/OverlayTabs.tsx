"use client";

import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { ChevronDown, ChevronLeft, ChevronRight } from "lucide-react";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { cn } from "@/lib/utils";
import { useMediaQuery } from "@/components/project/useMediaQuery";

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

// The exact count, same number the health strip and the tab's headline
// show; a cap like "99+" next to "205" reads as two different counts.
export function badgeText(n: number): string {
  return n.toLocaleString();
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
  // Phones: every section is a chip in one sideways-scrolling row (no
  // "More" popover, no select), pinned tab last.
  const phone = useMediaQuery("(max-width: 639px)");
  const visible = useMemo(
    () => (phone ? [...tabs, ...(pinned ? [pinned] : [])] : tabs.filter((t) => !t.secondary)),
    [tabs, pinned, phone],
  );
  const overflow = useMemo(() => (phone ? [] : tabs.filter((t) => t.secondary)), [tabs, phone]);
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

  const pageStrip = (dir: 1 | -1) => {
    const el = stripRef.current;
    if (!el) return;
    el.scrollBy({ left: dir * el.clientWidth * 0.7, behavior: "smooth" });
  };

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
          "relative inline-flex shrink-0 items-center gap-1.5 whitespace-nowrap text-sm font-medium transition-colors focus-visible:outline-offset-[-2px]",
          phone
            ? cn(
                "h-9 rounded-full border px-3.5",
                selected
                  ? "border-[var(--border-strong)] bg-[var(--bg-tertiary)] text-[var(--text-primary)]"
                  : "border-[var(--border-subtle)] text-[var(--text-secondary)]",
              )
            : cn(
                "h-10 px-3",
                selected ? "text-[var(--text-primary)]" : "text-[var(--text-secondary)] hover:text-[var(--text-primary)]",
                isPinned && "border-l border-[var(--border-subtle)] pl-3.5",
              ),
        )}
      >
        {Icon && <Icon aria-hidden className="h-3.5 w-3.5" />}
        {t.label}
        {!!t.badge && (
          <span className={cn("rounded-full px-1.5 text-xs font-semibold tabular-nums leading-[18px]", BADGE_TONE[t.badgeTone ?? "neutral"])}>
            {badgeText(t.badge)}
            <span className="sr-only"> {t.badgeTone === "error" ? "errors" : "items"}</span>
          </span>
        )}
        {selected && !phone && <span aria-hidden className="absolute inset-x-3 bottom-0 h-[2px] rounded-full bg-[var(--accent)]" />}
      </button>
    );
  };

  const overflowBadge = overflow.some((t) => !!t.badge && t.badgeTone === "error");

  return (
    <>
    <div
      role="tablist"
      aria-label={label}
      onKeyDown={onKeyDown}
      className={cn("relative flex shrink-0 items-center border-b border-[var(--border-subtle)] px-2 sm:px-3", phone && "py-2")}
    >
      <div className="relative flex min-w-0 flex-1">
        <div
          ref={stripRef}
          onScroll={updateFade}
          className={cn(
            "relative flex min-w-0 flex-1 items-center overflow-x-auto overscroll-x-contain [scrollbar-width:none] [&::-webkit-scrollbar]:hidden",
            phone && "gap-1.5 px-1",
          )}
        >
          {visible.map((t) => tabButton(t))}
        </div>
        {/* Clipped tabs get a fade plus a chevron, so the strip reads as
            scrollable; the chevron pages it (keyboard users have arrows). */}
        {fade.left && <ScrollEdge side="left" onPage={() => pageStrip(-1)} />}
        {fade.right && <ScrollEdge side="right" onPage={() => pageStrip(1)} />}
      </div>
      {overflow.length > 0 && (
            <Popover open={moreOpen} onOpenChange={setMoreOpen}>
              <PopoverTrigger
                data-more-trigger=""
                // While a secondary tab is active the trigger stands in for
                // it: same id (the panel's aria-labelledby resolves) and the
                // tablist's one tab stop.
                {...(activeInMore
                  ? {
                      role: "tab",
                      id: `${idPrefix}-tab-${activeInMore.id}`,
                      "aria-selected": true,
                      "aria-controls": `${idPrefix}-panel`,
                    }
                  : {})}
                tabIndex={activeInMore ? 0 : -1}
                aria-label={`More tabs: ${overflow.map((t) => t.label).join(", ")}${activeInMore ? ` (showing ${activeInMore.label})` : ""}`}
                className={cn(
                  "relative inline-flex h-10 shrink-0 items-center gap-1 whitespace-nowrap px-3 text-sm font-medium outline-none hover:text-[var(--text-primary)] focus-visible:text-[var(--text-primary)] focus-visible:underline",
                  activeInMore ? "text-[var(--text-primary)]" : "text-[var(--text-secondary)]",
                )}
              >
                {activeInMore ? activeInMore.label : "More"}
                {overflowBadge && <span aria-hidden className="h-1.5 w-1.5 rounded-full bg-[var(--error)]" />}
                <ChevronDown aria-hidden className="h-3.5 w-3.5" />
                {activeInMore && <span aria-hidden className="absolute inset-x-3 bottom-0 h-[2px] rounded-full bg-[var(--accent)]" />}
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
                          {badgeText(t.badge)}
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
      {pinned && !phone && tabButton(pinned, true)}
    </div>
    </>
  );
}

function ScrollEdge({ side, onPage }: { side: "left" | "right"; onPage: () => void }) {
  const Icon = side === "left" ? ChevronLeft : ChevronRight;
  return (
    <span
      className={cn(
        "pointer-events-none absolute inset-y-0 flex w-12 items-center from-[var(--bg-primary)] from-45% to-transparent",
        side === "left" ? "left-0 justify-start bg-gradient-to-r" : "right-0 justify-end bg-gradient-to-l",
      )}
    >
      <button
        type="button"
        tabIndex={-1}
        aria-hidden
        onClick={onPage}
        className="pointer-events-auto inline-flex h-7 w-7 items-center justify-center rounded-full border border-[var(--border-default)] bg-[var(--bg-elevated)] text-[var(--text-secondary)] shadow-sm hover:text-[var(--text-primary)]"
      >
        <Icon className="h-4 w-4" />
      </button>
    </span>
  );
}
