"use client";

import { TimeRangePicker } from "@/components/shared/TimeRangePicker";
import { timeRangeMs, timeRangeSpan } from "@/lib/time-range";

// Deprecated: kept only until ServiceMetricsPanel imports
// shared/TimeRangePicker and lib/time-range directly; delete this file then.
export function TimeRange({
  presets,
  value,
  onChange,
  className,
  "aria-label": ariaLabel = "Time range",
}: {
  presets: readonly string[];
  value: string;
  onChange: (v: string) => void;
  className?: string;
  "aria-label"?: string;
}) {
  return (
    <TimeRangePicker presets={presets} value={value} onChange={onChange} ariaLabel={ariaLabel} className={className} />
  );
}

export function rangeToMs(r: string): number {
  return timeRangeMs(r) ?? 0;
}

export function rangeLabel(r: string): string {
  return timeRangeSpan(r);
}
