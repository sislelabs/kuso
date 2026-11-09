"use client"

import * as React from "react"

import { cn } from "@/lib/utils"

type SegmentedOption<T extends string> = { value: T; label: React.ReactNode; title?: string }

type SegmentedProps<T extends string> = {
  options: readonly SegmentedOption<T>[]
  value: string
  onChange: (value: T) => void
  /** Accessible name of the group. */
  label: string
  size?: "sm" | "default"
  /** Monospace option text — only when the options are literal values. */
  mono?: boolean
  disabled?: boolean
  className?: string
}

// A single-choice control for 2–5 short options (radiogroup). Arrow keys
// move and select; the group is one tab stop. For time ranges use
// shared/TimeRangePicker, which is this plus range labels.
function Segmented<T extends string>({
  options,
  value,
  onChange,
  label,
  size = "default",
  mono = false,
  disabled = false,
  className,
}: SegmentedProps<T>) {
  const refs = React.useRef<(HTMLButtonElement | null)[]>([])
  const selected = Math.max(
    0,
    options.findIndex((o) => o.value === value)
  )
  const onKeyDown = (e: React.KeyboardEvent, i: number) => {
    let next = -1
    if (e.key === "ArrowRight" || e.key === "ArrowDown") next = (i + 1) % options.length
    else if (e.key === "ArrowLeft" || e.key === "ArrowUp") next = (i - 1 + options.length) % options.length
    if (next < 0) return
    e.preventDefault()
    refs.current[next]?.focus()
    onChange(options[next].value)
  }
  return (
    <div
      role="radiogroup"
      aria-label={label}
      aria-disabled={disabled || undefined}
      data-slot="segmented"
      className={cn(
        "inline-flex max-w-full flex-wrap items-center gap-0.5 rounded-md border border-[var(--border-default)] bg-[var(--bg-secondary)] p-0.5",
        disabled && "pointer-events-none opacity-50",
        className
      )}
    >
      {options.map((o, i) => {
        const checked = o.value === value
        return (
          <button
            key={o.value || "__none"}
            ref={(el) => {
              refs.current[i] = el
            }}
            type="button"
            role="radio"
            aria-checked={checked}
            title={o.title}
            tabIndex={i === selected ? 0 : -1}
            disabled={disabled}
            onClick={() => onChange(o.value)}
            onKeyDown={(e) => onKeyDown(e, i)}
            className={cn(
              "rounded-[calc(var(--radius-md)-2px)] font-medium whitespace-nowrap text-[var(--text-secondary)] transition-colors outline-none hover:text-[var(--text-primary)] focus-visible:ring-2 focus-visible:ring-[var(--focus)]",
              size === "sm" ? "h-6 px-2 text-xs" : "h-7 px-2.5 text-xs",
              mono && "font-mono",
              checked && "bg-[var(--bg-elevated)] text-[var(--text-primary)] shadow-[var(--shadow-sm),var(--highlight)]"
            )}
          >
            {o.label}
          </button>
        )
      })}
    </div>
  )
}

export { Segmented }
export type { SegmentedProps, SegmentedOption }
