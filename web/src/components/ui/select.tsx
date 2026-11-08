import * as React from "react"
import { ChevronDown } from "lucide-react"

import { cn } from "@/lib/utils"

// A styled native <select>. Deliberately not base-ui Select: its popup
// shares the portal path that broke base-ui Menu in the static export,
// and the native control is free keyboard/screen-reader/mobile support.
// Chrome matches <Input> so a form row of mixed fields reads as one set.

const selectSizes = {
  sm: "h-8 pl-2.5 pr-8 text-[13px]",
  default: "h-9 pl-3 pr-9 text-sm",
} as const

/** Class string for the control itself — for the rare call site that
 *  must keep a raw <select> (e.g. inside a third-party render prop). */
function selectClassName(size: keyof typeof selectSizes = "default") {
  return cn(
    "w-full min-w-0 cursor-pointer appearance-none truncate rounded-md border border-[var(--border-default)] bg-input text-[var(--text-primary)] transition-[border-color,box-shadow] outline-none hover:border-[var(--border-strong)] focus-visible:border-[var(--focus)] focus-visible:ring-3 focus-visible:ring-[var(--focus)]/25 disabled:cursor-not-allowed disabled:opacity-50 aria-invalid:border-[var(--danger)]",
    selectSizes[size]
  )
}

type SelectProps = Omit<React.ComponentProps<"select">, "size"> & {
  size?: keyof typeof selectSizes
  /** Classes for the wrapper (width, margin). `className` styles the control. */
  wrapperClassName?: string
  /** Rendered as a disabled, hidden first option when `value` is "". */
  placeholder?: string
}

function Select({
  className,
  wrapperClassName,
  size = "default",
  placeholder,
  children,
  ...props
}: SelectProps) {
  const showPlaceholder =
    placeholder !== undefined && (props.value === "" || props.value === undefined)
  return (
    <div data-slot="select-wrapper" className={cn("relative inline-flex w-full", wrapperClassName)}>
      <select
        data-slot="select"
        data-placeholder={showPlaceholder || undefined}
        className={cn(
          selectClassName(size),
          "data-placeholder:text-[var(--text-placeholder)]",
          className
        )}
        {...props}
      >
        {placeholder !== undefined && (
          <option value="" disabled hidden>
            {placeholder}
          </option>
        )}
        {children}
      </select>
      <ChevronDown
        aria-hidden
        className={cn(
          "pointer-events-none absolute top-1/2 -translate-y-1/2 text-[var(--text-tertiary)]",
          size === "sm" ? "right-2.5 size-3.5" : "right-3 size-4"
        )}
      />
    </div>
  )
}

export { Select, selectClassName }
export type { SelectProps }
