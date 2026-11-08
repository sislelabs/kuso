"use client"

import { cn } from "@/lib/utils"

type SwitchProps = {
  on: boolean
  onChange: (next: boolean) => void
  disabled?: boolean
  /** Accessible name when no visible label points at it. */
  label?: string
  labelledBy?: string
  describedBy?: string
  size?: "sm" | "default"
  className?: string
}

// A boolean toggle (role=switch). The knob takes the page background so
// it reads against the accent track in both themes.
function Switch({
  on,
  onChange,
  disabled,
  label,
  labelledBy,
  describedBy,
  size = "default",
  className,
}: SwitchProps) {
  const sm = size === "sm"
  return (
    <button
      type="button"
      role="switch"
      data-slot="switch"
      aria-checked={on}
      aria-label={label}
      aria-labelledby={labelledBy}
      aria-describedby={describedBy}
      disabled={disabled}
      onClick={() => onChange(!on)}
      className={cn(
        "relative inline-flex shrink-0 cursor-pointer items-center rounded-full border transition-colors outline-none disabled:cursor-not-allowed disabled:opacity-50",
        sm ? "h-4 w-7" : "h-5 w-9",
        on
          ? "border-transparent bg-[var(--accent)]"
          : "border-[var(--border-strong)] bg-[var(--bg-tertiary)]",
        className
      )}
    >
      <span
        aria-hidden
        className={cn(
          "inline-block rounded-full shadow-[var(--shadow-sm)] transition-transform",
          sm ? "size-2.5" : "size-3.5",
          on
            ? cn("bg-[var(--bg-primary)]", sm ? "translate-x-[14px]" : "translate-x-[18px]")
            : "translate-x-[2px] bg-[var(--text-tertiary)]"
        )}
      />
    </button>
  )
}

export { Switch }
export type { SwitchProps }
