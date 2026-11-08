"use client"

import * as React from "react"
import { Check, Copy } from "lucide-react"
import { toast } from "sonner"

import { cn } from "@/lib/utils"
import { copyText } from "@/lib/clipboard"

type CopyButtonProps = {
  value: string
  /** Accessible name and tooltip, e.g. "Copy DATABASE_URL". */
  label?: string
  /** Toast text after copying; `false` for the inline check only. */
  toastMessage?: string | false
  size?: "xs" | "sm"
  /** Render the label text next to the icon. */
  showLabel?: boolean
  className?: string
}

// One copy affordance for the whole app: icon swaps to a check for
// 1.5s and a polite live region announces it. Falls back to a hidden
// textarea when the async clipboard API is unavailable (plain-http
// instances).
function CopyButton({
  value,
  label = "Copy",
  toastMessage = false,
  size = "sm",
  showLabel = false,
  className,
}: CopyButtonProps) {
  const [copied, setCopied] = React.useState(false)
  const timer = React.useRef<number | undefined>(undefined)
  React.useEffect(() => () => window.clearTimeout(timer.current), [])

  const onClick = async (e: React.MouseEvent) => {
    e.stopPropagation()
    const ok = await copyText(value)
    if (!ok) {
      toast.error("Couldn't copy — your browser blocked clipboard access")
      return
    }
    setCopied(true)
    if (toastMessage) toast.success(toastMessage)
    window.clearTimeout(timer.current)
    timer.current = window.setTimeout(() => setCopied(false), 1500)
  }

  const Icon = copied ? Check : Copy
  return (
    <button
      type="button"
      data-slot="copy-button"
      onClick={onClick}
      aria-label={showLabel ? undefined : label}
      title={label}
      className={cn(
        "inline-flex shrink-0 items-center justify-center gap-1 rounded-md text-[var(--text-tertiary)] transition-colors hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] focus-visible:ring-2 focus-visible:ring-[var(--focus)] focus-visible:outline-none",
        size === "xs" ? "h-6 min-w-6 px-1 text-xs" : "h-7 min-w-7 px-1.5 text-xs",
        copied && "text-[var(--success)] hover:text-[var(--success)]",
        className
      )}
    >
      <Icon aria-hidden className={size === "xs" ? "size-3" : "size-3.5"} />
      {showLabel && <span>{copied ? "Copied" : label}</span>}
      <span aria-live="polite" className="sr-only">
        {copied ? "Copied" : ""}
      </span>
    </button>
  )
}

export { CopyButton }
export type { CopyButtonProps }
