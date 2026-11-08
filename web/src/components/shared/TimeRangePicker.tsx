"use client";

import * as React from "react";
import { cn } from "@/lib/utils";
import { DEFAULT_TIME_RANGE_PRESETS, timeRangeLabel } from "@/lib/time-range";

// One time-range control for metrics, logs, errors and usage.
//
//   <TimeRangePicker value={range} onChange={setRange} />
//   <TimeRangePicker presets={["1h", "24h", "7d"]} value={…} onChange={…} size="sm" />
//
// A segmented radiogroup: ←/→ move and select, the group is one tab
// stop. The strip scrolls horizontally rather than wrapping on phones.
export function TimeRangePicker<T extends string>({
  value,
  onChange,
  presets = DEFAULT_TIME_RANGE_PRESETS as unknown as readonly T[],
  size = "default",
  ariaLabel = "Time range",
  disabled = false,
  className,
}: {
  value: T;
  onChange: (value: T) => void;
  presets?: readonly T[];
  size?: "sm" | "default";
  ariaLabel?: string;
  disabled?: boolean;
  className?: string;
}) {
  const refs = React.useRef<(HTMLButtonElement | null)[]>([]);
  const onKeyDown = (e: React.KeyboardEvent, i: number) => {
    let next = -1;
    if (e.key === "ArrowRight" || e.key === "ArrowDown") next = (i + 1) % presets.length;
    else if (e.key === "ArrowLeft" || e.key === "ArrowUp") next = (i - 1 + presets.length) % presets.length;
    else if (e.key === "Home") next = 0;
    else if (e.key === "End") next = presets.length - 1;
    if (next < 0) return;
    e.preventDefault();
    refs.current[next]?.focus();
    onChange(presets[next]);
  };
  const selectedIndex = Math.max(0, presets.indexOf(value));
  return (
    <div
      role="radiogroup"
      aria-label={ariaLabel}
      aria-disabled={disabled || undefined}
      className={cn(
        "scrollbar-none inline-flex max-w-full shrink-0 items-center overflow-x-auto rounded-md border border-[var(--border-default)] bg-[var(--bg-secondary)] p-0.5",
        disabled && "pointer-events-none opacity-50",
        className,
      )}
    >
      {presets.map((p, i) => {
        const checked = p === value;
        return (
          <button
            key={p}
            ref={(el) => {
              refs.current[i] = el;
            }}
            type="button"
            role="radio"
            aria-checked={checked}
            aria-label={timeRangeLabel(p)}
            title={timeRangeLabel(p)}
            tabIndex={i === selectedIndex ? 0 : -1}
            disabled={disabled}
            onClick={() => onChange(p)}
            onKeyDown={(e) => onKeyDown(e, i)}
            className={cn(
              "shrink-0 rounded-[calc(var(--radius-md)-2px)] font-medium tabular-nums text-[var(--text-tertiary)] transition-colors outline-none hover:text-[var(--text-primary)] focus-visible:ring-2 focus-visible:ring-[var(--focus)]",
              size === "sm" ? "h-6 px-2 text-xs" : "h-7 px-2.5 text-[13px]",
              checked && "bg-[var(--bg-elevated)] text-[var(--text-primary)] shadow-[var(--shadow-sm)]",
            )}
          >
            {p}
          </button>
        );
      })}
    </div>
  );
}
