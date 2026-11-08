"use client";

import { useRef } from "react";
import type { KeyboardEvent, ReactNode } from "react";
import { X } from "lucide-react";
import { cn } from "@/lib/utils";

// TimeRange is the segmented range picker shared by logs, metrics and
// errors. Same {presets, value, onChange} shape as the planned
// components/shared/TimeRangePicker so the two can be swapped later.
// A value outside `presets` (an absolute window set from elsewhere) is
// shown as a dismissible custom chip via `customLabel`.
export function TimeRange({
  presets,
  value,
  onChange,
  labels,
  customLabel,
  onClearCustom,
  className,
  "aria-label": ariaLabel = "Time range",
}: {
  presets: readonly string[];
  value: string;
  onChange: (v: string) => void;
  labels?: Partial<Record<string, ReactNode>>;
  customLabel?: string;
  onClearCustom?: () => void;
  className?: string;
  "aria-label"?: string;
}) {
  const refs = useRef<(HTMLButtonElement | null)[]>([]);
  const active = presets.indexOf(value);

  // Radiogroup keyboard model: arrows move + select, focus follows.
  const onKey = (e: KeyboardEvent<HTMLDivElement>) => {
    if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(e.key)) return;
    e.preventDefault();
    const from = active < 0 ? 0 : active;
    const next =
      e.key === "Home"
        ? 0
        : e.key === "End"
          ? presets.length - 1
          : (from + (e.key === "ArrowRight" ? 1 : -1) + presets.length) % presets.length;
    onChange(presets[next]);
    refs.current[next]?.focus();
  };

  return (
    <div className={cn("flex min-w-0 items-center gap-1.5", className)}>
      {customLabel && (
        <span className="inline-flex h-8 shrink-0 items-center gap-1 rounded-md border border-[var(--border-default)] bg-[var(--bg-tertiary)] pl-2 pr-1 text-xs text-[var(--text-primary)]">
          {customLabel}
          {onClearCustom && (
            <button
              type="button"
              onClick={onClearCustom}
              aria-label="Clear custom time window"
              className="rounded p-0.5 text-[var(--text-tertiary)] hover:bg-[var(--bg-secondary)] hover:text-[var(--text-primary)]"
            >
              <X className="h-3 w-3" />
            </button>
          )}
        </span>
      )}
      <div
        role="radiogroup"
        aria-label={ariaLabel}
        onKeyDown={onKey}
        className="inline-flex h-8 shrink-0 items-center rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-0.5"
      >
        {presets.map((p, i) => {
          const on = p === value;
          return (
            <button
              key={p}
              ref={(el) => {
                refs.current[i] = el;
              }}
              type="button"
              role="radio"
              aria-checked={on}
              tabIndex={on || (active < 0 && i === 0) ? 0 : -1}
              onClick={() => onChange(p)}
              className={cn(
                "inline-flex h-full items-center gap-1 rounded px-2 text-xs tabular-nums transition-colors focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-[var(--ring)]",
                on
                  ? "bg-[var(--bg-tertiary)] font-medium text-[var(--text-primary)] shadow-[var(--shadow-sm)]"
                  : "text-[var(--text-secondary)] hover:text-[var(--text-primary)]",
              )}
            >
              {labels?.[p] ?? p}
            </button>
          );
        })}
      </div>
    </div>
  );
}

// rangeToMs parses "15m" / "6h" / "7d" into milliseconds (0 if unparseable).
export function rangeToMs(r: string): number {
  const m = /^(\d+)([mhdw])$/.exec(r);
  if (!m) return 0;
  const n = parseInt(m[1], 10);
  const unit = { m: 60_000, h: 3_600_000, d: 86_400_000, w: 604_800_000 }[m[2] as "m" | "h" | "d" | "w"];
  return n * unit;
}

export function rangeLabel(r: string): string {
  const m = /^(\d+)([mhdw])$/.exec(r);
  if (!m) return r;
  const n = parseInt(m[1], 10);
  const word = { m: "minute", h: "hour", d: "day", w: "week" }[m[2] as "m" | "h" | "d" | "w"];
  return `${n} ${word}${n === 1 ? "" : "s"}`;
}
