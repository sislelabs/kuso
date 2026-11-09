"use client"

import * as React from "react"
import { ChevronDown } from "lucide-react"

import { cn } from "@/lib/utils"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"

// Accessible tab bar with overflow handling.
//
//   <Tabs ariaLabel="Service" items={items} value={tab} onValueChange={setTab}
//         idBase="svc" pinnedEnd={["settings"]} />
//   <TabPanel idBase="svc" value="logs" active={tab === "logs"}>…</TabPanel>
//
// - WAI-ARIA tabs: role=tablist/tab/tabpanel, aria-selected, aria-controls,
//   roving tabindex, ←/→/Home/End move focus and select (automatic
//   activation).
// - overflow="more": tabs that don't fit collapse into a "More" popover
//   (measured with a ResizeObserver). The active tab is always kept
//   visible. `pinnedEnd` tabs never collapse (e.g. Settings).
// - overflow="scroll": one horizontally scrollable strip with edge fades.
// - overflow="auto" (default): "scroll" under 480px of available width,
//   "more" above it — phones get a swipeable strip, desktops a clean bar.

export type TabItem = {
  value: string
  label: React.ReactNode
  /** Plain-text name when `label` is a node; used in the More list and aria. */
  text?: string
  icon?: React.ReactNode
  /** Count or status shown after the label (e.g. error count). */
  badge?: React.ReactNode
  disabled?: boolean
}

type TabsProps = {
  items: readonly TabItem[]
  value: string
  onValueChange: (value: string) => void
  ariaLabel: string
  /** Prefix for tab/panel ids; pair with <TabPanel idBase>. */
  idBase?: string
  pinnedEnd?: readonly string[]
  overflow?: "auto" | "more" | "scroll"
  size?: "sm" | "default"
  className?: string
}

export const tabId = (idBase: string, value: string) => `${idBase}-tab-${value}`
export const tabPanelId = (idBase: string, value: string) => `${idBase}-panel-${value}`

const MORE_WIDTH = 84
const GAP = 4
const SCROLL_BELOW = 480

function tabClass(size: "sm" | "default") {
  return cn(
    "relative inline-flex shrink-0 items-center gap-1.5 rounded-md font-medium whitespace-nowrap outline-none transition-colors",
    "text-[var(--text-tertiary)] hover:text-[var(--text-primary)] hover:bg-[var(--bg-tertiary)]",
    "focus-visible:ring-2 focus-visible:ring-[var(--focus)] focus-visible:ring-inset",
    "aria-selected:text-[var(--text-primary)] aria-selected:hover:bg-transparent",
    "after:pointer-events-none after:absolute after:inset-x-2 after:-bottom-[5px] after:h-0.5 after:rounded-full after:bg-transparent aria-selected:after:bg-[var(--accent)]",
    "disabled:pointer-events-none disabled:opacity-50",
    "[&_svg]:size-3.5 [&_svg]:shrink-0",
    size === "sm" ? "h-7 px-2 text-[13px]" : "h-8 px-2.5 text-sm"
  )
}

function TabBadge({ children }: { children: React.ReactNode }) {
  return (
    <span className="inline-flex h-4 min-w-4 items-center justify-center rounded-full bg-[var(--bg-tertiary)] px-1 text-2xs font-medium tabular-nums text-[var(--text-secondary)]">
      {children}
    </span>
  )
}

function TabContent({ item }: { item: TabItem }) {
  return (
    <>
      {item.icon}
      <span>{item.label}</span>
      {item.badge !== undefined && item.badge !== null && item.badge !== false && (
        <TabBadge>{item.badge}</TabBadge>
      )}
    </>
  )
}

export function Tabs({
  items,
  value,
  onValueChange,
  ariaLabel,
  idBase: idBaseProp,
  pinnedEnd = [],
  overflow = "auto",
  size = "default",
  className,
}: TabsProps) {
  const autoId = React.useId().replace(/:/g, "")
  const idBase = idBaseProp ?? `tabs${autoId}`
  const containerRef = React.useRef<HTMLDivElement>(null)
  const measureRef = React.useRef<HTMLDivElement>(null)
  const scrollRef = React.useRef<HTMLDivElement>(null)
  const tabRefs = React.useRef(new Map<string, HTMLButtonElement>())
  const moreRef = React.useRef<HTMLButtonElement>(null)
  const [width, setWidth] = React.useState<number | null>(null)
  const [widths, setWidths] = React.useState<number[]>([])
  const [moreOpen, setMoreOpen] = React.useState(false)
  const [fade, setFade] = React.useState({ left: false, right: false })

  const pinnedSet = React.useMemo(() => new Set(pinnedEnd), [pinnedEnd])
  const flowItems = React.useMemo(() => items.filter((i) => !pinnedSet.has(i.value)), [items, pinnedSet])
  const pinnedItems = React.useMemo(() => items.filter((i) => pinnedSet.has(i.value)), [items, pinnedSet])

  React.useLayoutEffect(() => {
    const el = containerRef.current
    if (!el) return
    const measure = () => {
      setWidth(el.clientWidth)
      const m = measureRef.current
      if (m) setWidths(Array.from(m.children).map((c) => (c as HTMLElement).offsetWidth))
    }
    measure()
    const ro = new ResizeObserver(measure)
    ro.observe(el)
    return () => ro.disconnect()
  }, [items])

  const mode: "more" | "scroll" =
    overflow === "auto" ? (width !== null && width < SCROLL_BELOW ? "scroll" : "more") : overflow

  // Split flow items into visible + overflow for "more" mode.
  const { visible, hidden } = React.useMemo(() => {
    if (mode === "scroll" || width === null || widths.length !== items.length) {
      return { visible: flowItems, hidden: [] as TabItem[] }
    }
    const widthOf = new Map(items.map((it, i) => [it.value, widths[i] ?? 0]))
    const pinnedWidth = pinnedItems.reduce((s, it) => s + (widthOf.get(it.value) ?? 0) + GAP, 0)
    const total = flowItems.reduce((s, it) => s + (widthOf.get(it.value) ?? 0) + GAP, 0)
    if (total + pinnedWidth <= width) return { visible: flowItems, hidden: [] as TabItem[] }
    const budget = width - pinnedWidth - MORE_WIDTH
    const vis: TabItem[] = []
    let used = 0
    for (const it of flowItems) {
      const w = (widthOf.get(it.value) ?? 0) + GAP
      if (used + w > budget) break
      vis.push(it)
      used += w
    }
    // Keep the active tab on the bar: swap it in for the last visible one.
    const active = flowItems.find((i) => i.value === value)
    if (active && !vis.includes(active) && vis.length > 0) vis[vis.length - 1] = active
    else if (active && vis.length === 0) vis.push(active)
    const visSet = new Set(vis)
    return {
      visible: flowItems.filter((i) => visSet.has(i)),
      hidden: flowItems.filter((i) => !visSet.has(i)),
    }
  }, [mode, width, widths, items, flowItems, pinnedItems, value])

  const barItems = React.useMemo(() => [...visible, ...pinnedItems], [visible, pinnedItems])

  // Scroll mode: keep the active tab in view and maintain edge fades.
  const updateFade = React.useCallback(() => {
    const s = scrollRef.current
    if (!s || mode !== "scroll") return setFade({ left: false, right: false })
    setFade({ left: s.scrollLeft > 2, right: s.scrollLeft + s.clientWidth < s.scrollWidth - 2 })
  }, [mode])
  React.useEffect(() => {
    updateFade()
    if (mode !== "scroll") return
    tabRefs.current.get(value)?.scrollIntoView({ block: "nearest", inline: "nearest" })
  }, [mode, value, width, updateFade])

  const focusables = (): HTMLElement[] => {
    const tabs = barItems
      .filter((i) => !i.disabled)
      .map((i) => tabRefs.current.get(i.value))
      .filter((el): el is HTMLButtonElement => !!el)
    if (hidden.length > 0 && moreRef.current) {
      // DOM order: flow tabs, More, pinned tabs.
      const flowCount = visible.filter((i) => !i.disabled).length
      tabs.splice(flowCount, 0, moreRef.current)
    }
    return tabs
  }

  const onKeyDown = (e: React.KeyboardEvent) => {
    const keys = ["ArrowLeft", "ArrowRight", "Home", "End"]
    if (!keys.includes(e.key)) return
    const list = focusables()
    const idx = list.indexOf(document.activeElement as HTMLElement)
    if (idx === -1) return
    e.preventDefault()
    const next =
      e.key === "Home" ? 0
      : e.key === "End" ? list.length - 1
      : e.key === "ArrowRight" ? (idx + 1) % list.length
      : (idx - 1 + list.length) % list.length
    const el = list[next]
    el.focus()
    const v = el.dataset.value
    if (v) onValueChange(v)
  }

  const renderTab = (item: TabItem) => {
    const selected = item.value === value
    return (
      <button
        key={item.value}
        ref={(el) => {
          if (el) tabRefs.current.set(item.value, el)
          else tabRefs.current.delete(item.value)
        }}
        type="button"
        role="tab"
        id={tabId(idBase, item.value)}
        aria-controls={tabPanelId(idBase, item.value)}
        aria-selected={selected}
        tabIndex={selected ? 0 : -1}
        disabled={item.disabled}
        data-value={item.value}
        onClick={() => onValueChange(item.value)}
        className={tabClass(size)}
      >
        <TabContent item={item} />
      </button>
    )
  }

  const activeHidden = hidden.find((i) => i.value === value)
  const tablist = (
    <div role="tablist" aria-label={ariaLabel} aria-orientation="horizontal" onKeyDown={onKeyDown} className="flex items-center gap-1">
      {visible.map(renderTab)}
      {hidden.length > 0 && (
        <Popover open={moreOpen} onOpenChange={setMoreOpen}>
          <PopoverTrigger
            ref={moreRef}
            tabIndex={-1}
            aria-label={`More tabs (${hidden.length})`}
            className={cn(tabClass(size), activeHidden && "text-[var(--text-primary)]")}
          >
            {activeHidden ? activeHidden.text ?? activeHidden.label : "More"}
            <ChevronDown aria-hidden />
          </PopoverTrigger>
          <PopoverContent align="end" className="w-48 gap-0 p-1">
            {hidden.map((item) => (
              <button
                key={item.value}
                type="button"
                disabled={item.disabled}
                aria-current={item.value === value ? "true" : undefined}
                onClick={() => {
                  onValueChange(item.value)
                  setMoreOpen(false)
                }}
                className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] aria-[current=true]:bg-[var(--selected)] aria-[current=true]:text-[var(--text-primary)] disabled:opacity-50 [&_svg]:size-3.5"
              >
                <TabContent item={item} />
              </button>
            ))}
          </PopoverContent>
        </Popover>
      )}
      {pinnedItems.map(renderTab)}
    </div>
  )

  return (
    <div ref={containerRef} data-slot="tabs" className={cn("relative min-w-0 border-b border-[var(--border-subtle)]", mode === "more" && "pb-1", className)}>
      {/* Invisible copy of every tab, used only to measure natural widths. */}
      <div
        ref={measureRef}
        aria-hidden
        inert
        className="pointer-events-none invisible absolute top-0 left-0 flex gap-1 whitespace-nowrap"
      >
        {items.map((item) => (
          <span key={item.value} className={tabClass(size)}>
            <TabContent item={item} />
          </span>
        ))}
      </div>
      {mode === "scroll" ? (
        <div className="relative">
          <div ref={scrollRef} onScroll={updateFade} className="scrollbar-none overflow-x-auto pb-[5px]">
            {tablist}
          </div>
          <div
            aria-hidden
            className={cn(
              "pointer-events-none absolute inset-y-0 left-0 w-8 bg-gradient-to-r from-[var(--bg-primary)] to-transparent transition-opacity",
              fade.left ? "opacity-100" : "opacity-0"
            )}
          />
          <div
            aria-hidden
            className={cn(
              "pointer-events-none absolute inset-y-0 right-0 w-8 bg-gradient-to-l from-[var(--bg-primary)] to-transparent transition-opacity",
              fade.right ? "opacity-100" : "opacity-0"
            )}
          />
        </div>
      ) : (
        tablist
      )}
    </div>
  )
}

export function TabPanel({
  idBase,
  value,
  active,
  className,
  children,
  keepMounted = false,
}: {
  idBase: string
  value: string
  active: boolean
  className?: string
  children: React.ReactNode
  /** Keep the panel in the DOM (hidden) when inactive, preserving state. */
  keepMounted?: boolean
}) {
  if (!active && !keepMounted) return null
  return (
    <div
      role="tabpanel"
      id={tabPanelId(idBase, value)}
      aria-labelledby={tabId(idBase, value)}
      hidden={!active}
      tabIndex={0}
      className={cn("outline-none focus-visible:ring-2 focus-visible:ring-[var(--focus)]", className)}
    >
      {children}
    </div>
  )
}
